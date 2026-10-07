// Package ipset replaces Coraza's @ipMatchFromFile operator (and its
// @ipMatchF alias) with one that finds an address by binary search over
// sorted ranges. Coraza v3 compares the address with every network of the
// list, so each lookup costs time in proportion to the list; here it costs
// O(log n), and the parsed list is shared by every WAF that loads the same
// file content.
//
// Parsing and matching follow Coraza v3's ipMatch exactly: the same
// tokenization and net.ParseCIDR, entries that do not parse are skipped,
// an empty list matches nothing, and IPv4-mapped addresses match IPv4
// networks. Import the package for its side effect.
package ipset

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/corazawaf/coraza/v3/experimental/plugins"
	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
)

func init() {
	// Coraza registers its operators before this package initializes, so
	// these registrations replace them.
	plugins.RegisterOperator("ipMatchFromFile", newFromFile)
	plugins.RegisterOperator("ipMatchF", newFromFile)
}

// u128 is an IPv6 address as a number.
type u128 struct{ hi, lo uint64 }

func (a u128) less(b u128) bool { return a.hi < b.hi || a.hi == b.hi && a.lo < b.lo }

func (a u128) add1() u128 {
	if a.lo == ^uint64(0) {
		return u128{a.hi + 1, 0}
	}
	return u128{a.hi, a.lo + 1}
}

func toU128(b []byte) u128 {
	return u128{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
}

// span4 and span6 are inclusive address ranges.
type (
	span4 struct{ from, to uint32 }
	span6 struct{ from, to u128 }
)

// set holds sorted ranges that neither overlap nor touch, per family. It is
// immutable once built.
type set struct {
	v4 []span4
	v6 []span6
}

var _ plugintypes.Operator = (*set)(nil)

var errEmptyDirs = errors.New("empty dirs")

// parseList builds a set; tests replace it to count parsing.
var parseList = parse

func newFromFile(options plugintypes.OperatorOptions) (plugintypes.Operator, error) {
	data, err := load(options.Arguments, options.Path, options.Root)
	if err != nil {
		return nil, err
	}
	if options.Memoizer == nil {
		return parseList(data), nil
	}
	// The key is the content, not the path: a file edited in place
	// between reloads must not reuse the old list.
	sum := sha256.Sum256(data)
	v, err := options.Memoizer.Do("caddy-with-auth/ipset:"+hex.EncodeToString(sum[:]), func() (any, error) {
		return parseList(data), nil
	})
	if err != nil {
		return nil, err
	}
	if s, ok := v.(*set); ok {
		return s, nil
	}
	return parseList(data), nil
}

// load reads the list like Coraza's operators do: an absolute path from the
// root file system, a relative one from the first of dirs that holds it.
func load(name string, dirs []string, root fs.FS) ([]byte, error) {
	if path.IsAbs(name) {
		return fs.ReadFile(root, name)
	}
	if len(dirs) == 0 {
		return nil, errEmptyDirs
	}
	var err error
	for _, dir := range dirs {
		var content []byte
		content, err = fs.ReadFile(root, path.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		return content, nil
	}
	return nil, err
}

// parse builds the set from list content with Coraza's ipMatchFromFile
// tokenization: trimmed lines, blank and '#' lines skipped, comma-separated
// entries, /32 or /128 added to bare addresses.
func parse(data []byte) *set {
	var entries []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	// Like Coraza, a scanner error (a line over 64 KiB) ends the list.
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		entries = append(entries, strings.Split(line, ",")...)
	}
	s := &set{}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, ":") && !strings.Contains(entry, "/") {
			entry += "/128"
		} else if strings.Contains(entry, ".") && !strings.Contains(entry, "/") {
			entry += "/32"
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			continue
		}
		s.add(network)
	}
	s.v4, s.v6 = merge4(s.v4), merge6(s.v6)
	return s
}

// add records the addresses that network.Contains accepts. A network whose
// address converts to IPv4 (an IPv4-mapped one included) matches IPv4
// addresses through the last four bytes of a 16-byte mask.
func (s *set) add(network *net.IPNet) {
	mask := network.Mask
	if ip := network.IP.To4(); ip != nil {
		if len(mask) == net.IPv6len {
			mask = mask[12:]
		}
		if len(mask) != net.IPv4len {
			return // net.IPNet.Contains never matches it
		}
		m := binary.BigEndian.Uint32(mask)
		from := binary.BigEndian.Uint32(ip) & m
		s.v4 = append(s.v4, span4{from, from | ^m})
		return
	}
	if len(network.IP) != net.IPv6len || len(mask) != net.IPv6len {
		return // net.IPNet.Contains never matches it
	}
	m, ip := toU128(mask), toU128(network.IP)
	from := u128{ip.hi & m.hi, ip.lo & m.lo}
	s.v6 = append(s.v6, span6{from, u128{from.hi | ^m.hi, from.lo | ^m.lo}})
}

func merge4(in []span4) []span4 {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].from < in[j].from })
	out := in[:1]
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s.from <= last.to || s.from == last.to+1 {
			last.to = max(last.to, s.to)
			continue
		}
		out = append(out, s)
	}
	// Copy: the input array is sized for every network before merging.
	return append(make([]span4, 0, len(out)), out...)
}

func merge6(in []span6) []span6 {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].from.less(in[j].from) })
	out := in[:1]
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if !last.to.less(s.from) || s.from == last.to.add1() {
			if last.to.less(s.to) {
				last.to = s.to
			}
			continue
		}
		out = append(out, s)
	}
	return append(make([]span6, 0, len(out)), out...)
}

// Evaluate reports whether value is an address in the set, with the
// address parsing of Coraza's ipMatch.
func (s *set) Evaluate(_ plugintypes.TransactionState, value string) bool {
	ip := net.ParseIP(value)
	if ip4 := ip.To4(); ip4 != nil {
		v := binary.BigEndian.Uint32(ip4)
		i := sort.Search(len(s.v4), func(i int) bool { return s.v4[i].to >= v })
		return i < len(s.v4) && s.v4[i].from <= v
	}
	if len(ip) != net.IPv6len {
		return false
	}
	v := toU128(ip)
	i := sort.Search(len(s.v6), func(i int) bool { return !s.v6[i].to.less(v) })
	return i < len(s.v6) && !v.less(s.v6[i].from)
}
