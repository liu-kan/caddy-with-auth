// Review govulncheck's JSON stream. JSON mode itself does not fail on findings.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

type finding struct {
	ID           string `json:"osv"`
	FixedVersion string `json:"fixed_version"`
	Trace        []struct {
		Module  string `json:"module"`
		Version string `json:"version"`
	} `json:"trace"`
}

type message struct {
	Config *struct {
		ScannerName string `json:"scanner_name"`
		ScanMode    string `json:"scan_mode"`
		ScanLevel   string `json:"scan_level"`
	} `json:"config"`
	SBOM *struct {
		GoVersion string            `json:"go_version"`
		Modules   []json.RawMessage `json:"modules"`
	} `json:"SBOM"`
	OSV *struct {
		ID      string   `json:"id"`
		Summary string   `json:"summary"`
		Aliases []string `json:"aliases"`
	} `json:"osv"`
	Finding *finding `json:"finding"`
}

func reviewReport(input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(input)
	configured := false
	analyzed := false
	findings := map[string]finding{}
	details := map[string]string{}
	for {
		var record message
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("invalid vulnerability report: %w", err)
		}
		if record.Config != nil {
			if record.Config.ScannerName != "govulncheck" || record.Config.ScanMode != "binary" || record.Config.ScanLevel != "module" {
				return fmt.Errorf("expected a govulncheck binary scan at module level")
			}
			configured = true
		}
		if record.OSV != nil {
			details[record.OSV.ID] = fmt.Sprintf("%s %v", record.OSV.Summary, record.OSV.Aliases)
		}
		if record.SBOM != nil {
			analyzed = record.SBOM.GoVersion != "" && len(record.SBOM.Modules) > 0
		}
		if record.Finding != nil {
			value := *record.Finding
			if value.ID == "" || len(value.Trace) == 0 || value.Trace[0].Module == "" {
				return fmt.Errorf("incomplete vulnerability finding")
			}
			key := value.ID + " " + value.Trace[0].Module
			if prior, exists := findings[key]; !exists || value.FixedVersion != "" || prior.FixedVersion == "" {
				findings[key] = value
			}
		}
	}
	if !configured || !analyzed {
		return fmt.Errorf("vulnerability report is missing scanner configuration or binary inventory")
	}
	keys := make([]string, 0, len(findings))
	for key := range findings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fixable, unfixed := 0, 0
	for _, key := range keys {
		value := findings[key]
		frame := value.Trace[0]
		if value.FixedVersion == "" {
			unfixed++
			fmt.Fprintf(output, "UNFIXED: %s %s@%s; no published fix\n", value.ID, frame.Module, frame.Version)
		} else {
			fixable++
			fmt.Fprintf(output, "BLOCKED: %s %s@%s; fixed in %s\n", value.ID, frame.Module, frame.Version, value.FixedVersion)
		}
		fmt.Fprintf(output, "  %s\n  https://pkg.go.dev/vuln/%s\n", details[value.ID], value.ID)
	}
	fmt.Fprintf(output, "Vulnerability summary: %d fixable, %d without a published fix\n", fixable, unfixed)
	if fixable > 0 {
		return fmt.Errorf("refusing to publish: %d vulnerabilities have published fixes", fixable)
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: check-caddy-vulnerabilities <govulncheck-json-report>")
		os.Exit(2)
	}
	input, err := os.Open(os.Args[1])
	if err == nil {
		defer input.Close()
		err = reviewReport(input, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vulnerability check failed:", err)
		os.Exit(1)
	}
}
