package ipset

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/experimental"
	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
)

// reference is Coraza v3.8.0's ipMatchFromFile (internal/operators
// ip_match_from_file.go and ip_match.go): the behavior the set reproduces.
type reference struct{ subnets []net.IPNet }

func newReference(data []byte) *reference {
	dataParsed := strings.Builder{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		l := sc.Text()
		l = strings.TrimSpace(l)
		if len(l) == 0 {
			continue
		}
		if l[0] == '#' {
			continue
		}
		dataParsed.WriteString(",")
		dataParsed.WriteString(l)
	}
	var subnets []net.IPNet
	for _, sb := range strings.Split(dataParsed.String(), ",") {
		sb = strings.TrimSpace(sb)
		if sb == "" {
			continue
		}
		if strings.Contains(sb, ":") && !strings.Contains(sb, "/") {
			sb += "/128"
		} else if strings.Contains(sb, ".") && !strings.Contains(sb, "/") {
			sb += "/32"
		}
		_, subnet, err := net.ParseCIDR(sb)
		if err != nil {
			continue
		}
		subnets = append(subnets, *subnet)
	}
	return &reference{subnets: subnets}
}

func (o *reference) match(value string) bool {
	ip := net.ParseIP(value)
	for _, subnet := range o.subnets {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

// corazaSources are the Coraza files whose behavior this package
// reproduces, with their sha256 in Coraza v3.8.0. A Coraza release that
// changes them fails this test: compare the change with reference and with
// parse and Evaluate, update them, then record the new hashes.
var corazaSources = map[string]string{
	"internal/operators/ip_match.go":           "558b9bffb7cdefb03c012468d92cdeb0d0ecd1b73607b2c98f5ba3d47d78f3b3",
	"internal/operators/ip_match_from_file.go": "7f5dc9a61876b2172e99019fa920aa0887f42264f8d184d180b870f68fb62797",
	"internal/operators/from_file.go":          "cebf682e93931a2da4ed30bf0f8903780157baabcab1bc001ac05d52ed68263d",
}

func TestCorazaIPMatchIsUnchanged(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/corazawaf/coraza/v3").Output()
	if err != nil {
		t.Skipf("cannot locate the Coraza module: %v", err)
	}
	dir := strings.TrimSpace(string(out))
	for name, want := range corazaSources {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("Coraza changed %s (sha256 %s): review the plugin against it", name, got)
		}
	}
}

func TestMatchesCorazaOnEdgeCases(t *testing.T) {
	list := `# comment
192.0.2.0/24
  198.51.100.7
203.0.113.0/25,203.0.113.128/25
10.1.2.3/8
2001:db8::/32
2001:db8:ffff::1
::ffff:100.64.0.0/112
300.1.1.1
1.2.3.4 # trailing note
fe80::1%eth0
not-an-address

172.16.0.0/12, 172.16.0.0/16
`
	s, ref := parse([]byte(list)), newReference([]byte(list))
	queries := []string{
		"192.0.2.0", "192.0.2.255", "192.0.3.0", "192.0.1.255", "198.51.100.7", "198.51.100.8",
		"203.0.113.0", "203.0.113.200", "10.255.255.255", "11.0.0.0", "9.255.255.255",
		"2001:db8::", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "2001:db9::", "2001:db7:ffff:ffff:ffff:ffff:ffff:ffff",
		"2001:db8:ffff::1", "::ffff:192.0.2.1", "::ffff:c000:201", "100.64.0.5", "100.64.255.255", "100.65.0.0",
		"::ffff:100.64.0.5", "1.2.3.4", "300.1.1.1", "fe80::1", "fe80::1%eth0", "", "192.0.2.1:80", "0.0.0.0",
		"255.255.255.255", "::", "::1", "172.31.255.255", "172.32.0.0", " 192.0.2.1", "192.0.2.1 ",
	}
	for _, q := range queries {
		if got, want := s.Evaluate(nil, q), ref.match(q); got != want {
			t.Errorf("%q: set %v, Coraza %v", q, got, want)
		}
	}
	for q, want := range map[string]bool{
		"192.0.2.255": true, "192.0.3.0": false, "203.0.113.200": true, "10.0.0.1": true,
		"::ffff:192.0.2.1": true, "2001:db8:1::": true, "1.2.3.4": false, "": false, "not-an-address": false,
	} {
		if got := s.Evaluate(nil, q); got != want {
			t.Errorf("%q: got %v, want %v", q, got, want)
		}
	}
}

func TestEmptyListsMatchNothing(t *testing.T) {
	for _, list := range []string{"", "# only a comment\n\n", "300.1.1.1\nnot-an-address\n"} {
		s := parse([]byte(list))
		for _, q := range []string{"0.0.0.0", "192.0.2.1", "::", "2001:db8::1"} {
			if s.Evaluate(nil, q) || newReference([]byte(list)).match(q) {
				t.Fatalf("list %q must not match %q", list, q)
			}
		}
	}
}

// lastAddr returns the last address of a masked prefix.
func lastAddr(p netip.Prefix) netip.Addr {
	if p.Addr().Is4() {
		b := p.Addr().As4()
		v := binary.BigEndian.Uint32(b[:]) | ^uint32(0)>>p.Bits()
		if p.Bits() == 32 {
			v = binary.BigEndian.Uint32(b[:])
		}
		binary.BigEndian.PutUint32(b[:], v)
		return netip.AddrFrom4(b)
	}
	b := p.Addr().As16()
	for i := p.Bits(); i < 128; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	return netip.AddrFrom16(b)
}

func randomPrefix(rnd *rand.Rand) netip.Prefix {
	switch rnd.IntN(3) {
	case 0:
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], rnd.Uint32())
		return netip.PrefixFrom(netip.AddrFrom4(a), rnd.IntN(33)).Masked()
	case 1:
		// A small IPv6 space so that networks overlap and touch.
		var a [16]byte
		copy(a[:4], []byte{0x20, 0x01, 0x0d, 0xb8})
		binary.BigEndian.PutUint64(a[8:], rnd.Uint64()&0xffff)
		return netip.PrefixFrom(netip.AddrFrom16(a), 32+rnd.IntN(97)).Masked()
	default:
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], rnd.Uint32())
		return netip.PrefixFrom(netip.AddrFrom16(netip.AddrFrom4(a).As16()), 80+rnd.IntN(49)).Masked()
	}
}

func TestRandomListsMatchCoraza(t *testing.T) {
	rnd := rand.New(rand.NewPCG(7, 11))
	for round := 0; round < 30; round++ {
		var list strings.Builder
		var queries []string
		for i := 0; i < 200; i++ {
			p := randomPrefix(rnd)
			switch {
			case p.IsSingleIP() && rnd.IntN(2) == 0:
				list.WriteString(p.Addr().String()) // a bare address
			case rnd.IntN(10) == 0:
				list.WriteString(p.String() + "," + randomPrefix(rnd).String())
			default:
				list.WriteString(p.String())
			}
			list.WriteString("\n")
			first, last := p.Addr(), lastAddr(p)
			queries = append(queries, first.String(), last.String(), first.Prev().String(), last.Next().String())
			if first.Is4() {
				queries = append(queries, "::ffff:"+first.String())
			}
		}
		for i := 0; i < 400; i++ {
			queries = append(queries, lastAddr(randomPrefix(rnd)).String())
		}
		data := []byte(list.String())
		s, ref := parse(data), newReference(data)
		for _, q := range queries {
			if got, want := s.Evaluate(nil, q), ref.match(q); got != want {
				t.Fatalf("round %d, %q: set %v, Coraza %v\nlist:\n%s", round, q, got, want, data)
			}
		}
	}
}

func FuzzMatchesCoraza(f *testing.F) {
	f.Add("192.0.2.0/24\n2001:db8::/32\n", "192.0.2.7")
	f.Add("::ffff:10.0.0.0/104", "10.1.2.3")
	f.Add("# c\n1.2.3.4,5.6.7.8\n", "::ffff:5.6.7.8")
	f.Add("0.0.0.0/0\n::/0\n", "::1")
	f.Add("10.0.0.0/8\n10.0.0.0/9\n", "10.200.0.1")
	f.Fuzz(func(t *testing.T, list, query string) {
		if got, want := parse([]byte(list)).Evaluate(nil, query), newReference([]byte(list)).match(query); got != want {
			t.Fatalf("list %q, query %q: set %v, Coraza %v", list, query, got, want)
		}
	})
}

func TestRelativeNamesSearchTheConfigDirectories(t *testing.T) {
	root := fstest.MapFS{"conf/list.txt": {Data: []byte("192.0.2.0/24\n")}}
	op, err := newFromFile(plugintypes.OperatorOptions{Arguments: "list.txt", Path: []string{"missing", "conf"}, Root: root})
	if err != nil || !op.Evaluate(nil, "192.0.2.9") || op.Evaluate(nil, "192.0.3.1") {
		t.Fatalf("op %v, err %v", op, err)
	}
	if _, err := newFromFile(plugintypes.OperatorOptions{Arguments: "list.txt", Root: root}); !errors.Is(err, errEmptyDirs) {
		t.Fatalf("a relative name without directories: %v", err)
	}
	if _, err := newFromFile(plugintypes.OperatorOptions{Arguments: "absent.txt", Path: []string{"conf"}, Root: root}); !os.IsNotExist(err) {
		t.Fatalf("a missing list: %v", err)
	}
}

// countParses counts the lists parsed while the test runs.
func countParses(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	old := parseList
	parseList = func(data []byte) *set {
		n.Add(1)
		return old(data)
	}
	t.Cleanup(func() { parseList = old })
	return &n
}

func writeList(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list.txt")
	// The test name keeps the content, and so the cache key, unique.
	if err := os.WriteFile(path, []byte("# "+t.Name()+"\n"+content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newWAF(t *testing.T, directives string) coraza.WAF {
	t.Helper()
	waf, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives(directives))
	if err != nil {
		t.Fatal(err)
	}
	return waf
}

func closeWAF(t *testing.T, waf coraza.WAF) {
	t.Helper()
	if err := waf.(experimental.WAFCloser).Close(); err != nil {
		t.Fatal(err)
	}
}

func denied(t *testing.T, waf coraza.WAF, client string) bool {
	t.Helper()
	tx := waf.NewTransaction()
	defer func() { _ = tx.Close() }()
	tx.ProcessConnection(client, 40000, "127.0.0.1", 80)
	tx.ProcessURI("/", "GET", "HTTP/1.1")
	return tx.ProcessRequestHeaders() != nil
}

func TestReplacesCorazaOperators(t *testing.T) {
	n := countParses(t)
	list := writeList(t, "203.0.113.0/24\n2001:db8::/32\n")
	inside := newWAF(t, "SecRuleEngine On\nSecRule REMOTE_ADDR \"@ipMatchFromFile "+list+"\" \"id:1,phase:1,deny,status:403\"")
	defer closeWAF(t, inside)
	outside := newWAF(t, "SecRuleEngine On\nSecRule REMOTE_ADDR \"!@ipMatchF "+list+"\" \"id:1,phase:1,deny,status:403\"")
	defer closeWAF(t, outside)
	if n.Load() != 1 {
		t.Fatalf("both operator names must use the plugin and share one parsed list: %d parses", n.Load())
	}
	for client, member := range map[string]bool{
		"203.0.113.9": true, "::ffff:203.0.113.9": true, "2001:db8::5": true, "198.51.100.1": false, "2001:db9::1": false,
	} {
		if denied(t, inside, client) != member || denied(t, outside, client) == member {
			t.Errorf("%s: member %v", client, member)
		}
	}
}

func TestClosingTheLastWAFReleasesTheList(t *testing.T) {
	n := countParses(t)
	rule := "SecRuleEngine On\nSecRule REMOTE_ADDR \"@ipMatchFromFile " + writeList(t, "192.0.2.0/24\n") + "\" \"id:1,phase:1,deny,status:403\""
	first, second := newWAF(t, rule), newWAF(t, rule)
	closeWAF(t, first)
	third := newWAF(t, rule)
	if n.Load() != 1 {
		t.Fatalf("a list in use must stay cached: %d parses", n.Load())
	}
	closeWAF(t, second)
	closeWAF(t, third)
	defer closeWAF(t, newWAF(t, rule))
	if n.Load() != 2 {
		t.Fatalf("closing every WAF must release the list: %d parses", n.Load())
	}
}

func TestMissingListFailsLikeCoraza(t *testing.T) {
	_, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives(
		"SecRule REMOTE_ADDR \"@ipMatchFromFile " + filepath.Join(t.TempDir(), "absent.txt") + "\" \"id:1,phase:1,deny\""))
	if err == nil {
		t.Fatal("a missing list must fail the WAF")
	}
}

// benchmarkList returns n random networks of both families and addresses
// that are mostly outside them, the worst case for a linear scan.
func benchmarkList(n int) ([]byte, []string) {
	rnd := rand.New(rand.NewPCG(1, 2))
	var b strings.Builder
	for i := 0; i < n; i++ {
		var a [16]byte
		if i%2 == 0 {
			binary.BigEndian.PutUint32(a[12:], rnd.Uint32())
			b.WriteString(netip.PrefixFrom(netip.AddrFrom4([4]byte(a[12:])), 16+rnd.IntN(9)).Masked().String())
		} else {
			binary.BigEndian.PutUint64(a[:8], 0x2000000000000000|rnd.Uint64()>>3)
			b.WriteString(netip.PrefixFrom(netip.AddrFrom16(a), 32+rnd.IntN(17)).Masked().String())
		}
		b.WriteString("\n")
	}
	queries := make([]string, 1024)
	for i := range queries {
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], rnd.Uint32())
		queries[i] = netip.AddrFrom4(a).String()
	}
	return []byte(b.String()), queries
}

func BenchmarkSet(b *testing.B) {
	for _, n := range []int{10_000, 280_000} {
		data, queries := benchmarkList(n)
		s := parse(data)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			for i := 0; b.Loop(); i++ {
				s.Evaluate(nil, queries[i%len(queries)])
			}
		})
	}
}

func BenchmarkCorazaLinear(b *testing.B) {
	for _, n := range []int{10_000, 280_000} {
		data, queries := benchmarkList(n)
		ref := newReference(data)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			for i := 0; b.Loop(); i++ {
				ref.match(queries[i%len(queries)])
			}
		})
	}
}

func BenchmarkParse280k(b *testing.B) {
	data, _ := benchmarkList(280_000)
	for b.Loop() {
		parse(data)
	}
}
