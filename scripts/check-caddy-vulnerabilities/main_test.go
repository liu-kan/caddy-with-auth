package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVulnerabilityPublicationPolicy(t *testing.T) {
	config := `{"config":{"scanner_name":"govulncheck","scan_mode":"binary","scan_level":"module"}}{"SBOM":{"go_version":"go1.27.1","modules":[{"path":"caddy"}]}}`
	unfixed := `{"finding":{"osv":"GO-UNFIXED","trace":[{"module":"example.org/unfixed","version":"v1.0.0"}]}}`
	fixed := `{"finding":{"osv":"GO-FIXABLE","fixed_version":"v3.3.3","trace":[{"module":"github.com/corazawaf/coraza/v3","version":"v3.3.2"}]}}`
	for _, test := range []struct {
		name       string
		report     string
		wantError  bool
		wantOutput string
	}{
		{"clean", config, false, "0 fixable, 0 without"},
		{"unfixed reported", config + unfixed, false, "UNFIXED: GO-UNFIXED"},
		{"fixable blocked", config + fixed, true, "BLOCKED: GO-FIXABLE"},
		{"mixed blocked", config + unfixed + fixed, true, "1 fixable, 1 without"},
		{"duplicate deduplicated", config + fixed + fixed, true, "1 fixable, 0 without"},
		{"empty scan", "", true, ""},
		{"missing config", unfixed, true, ""},
		{"missing inventory", `{"config":{"scanner_name":"govulncheck","scan_mode":"binary","scan_level":"module"}}`, true, ""},
		{"malformed JSON", config + `{`, true, ""},
		{"incomplete finding", config + `{"finding":{"osv":"GO-FAIL"}}`, true, ""},
		{"wrong scan level", `{"config":{"scanner_name":"govulncheck","scan_mode":"binary","scan_level":"symbol"}}`, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := reviewReport(strings.NewReader(test.report), &output)
			if (err != nil) != test.wantError {
				t.Fatalf("reviewReport() = %v; wantError %v", err, test.wantError)
			}
			if !strings.Contains(output.String(), test.wantOutput) {
				t.Fatalf("report %q missing %q", output.String(), test.wantOutput)
			}
		})
	}
}
