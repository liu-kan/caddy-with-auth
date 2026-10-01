// Check the dependency versions embedded in the Caddy binary we publish.
package main

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	legacyPlugin  = "github.com/corazawaf/coraza-caddy"
	currentPlugin = legacyPlugin + "/v2"
	corazaModule  = "github.com/corazawaf/coraza/v3"
	// Covers CVE-2023-40586 (v3.0.1) and CVE-2025-29914 (v3.3.3).
	minimumCoraza = "v3.3.3"
)

// Only stable semantic versions are accepted for the security-sensitive modules.
func stableVersion(version string) ([3]int, bool) {
	var result [3]int
	if !strings.HasPrefix(version, "v") {
		return result, false
	}
	core := strings.SplitN(strings.TrimPrefix(version, "v"), "+", 2)[0]
	if strings.Contains(core, "-") {
		return result, false
	}
	parts := strings.Split(core, ".")
	if len(parts) != len(result) {
		return result, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return result, false
			}
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return result, false
		}
		result[i] = number
	}
	return result, true
}

func atLeast(version, minimum string) bool {
	actual, ok := stableVersion(version)
	if !ok {
		return false
	}
	floor, ok := stableVersion(minimum)
	if !ok {
		return false
	}
	for i := range actual {
		if actual[i] != floor[i] {
			return actual[i] > floor[i]
		}
	}
	return true
}

func checkDependencies(info *debug.BuildInfo) error {
	required := map[string]string{
		currentPlugin: "v2.0.0",
		corazaModule:  minimumCoraza,
	}
	for _, dependency := range info.Deps {
		if dependency.Path == legacyPlugin {
			return fmt.Errorf("obsolete plugin %s found; use %s", legacyPlugin, currentPlugin)
		}
		minimum, needed := required[dependency.Path]
		if !needed {
			continue
		}
		if dependency.Replace != nil {
			return fmt.Errorf("cannot verify replaced security module %s", dependency.Path)
		}
		if !atLeast(dependency.Version, minimum) {
			return fmt.Errorf("%s is %s; require a stable version >= %s", dependency.Path, dependency.Version, minimum)
		}
		delete(required, dependency.Path)
	}
	if len(required) != 0 {
		return fmt.Errorf("required security modules missing from binary: %v", required)
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: check-caddy-dependencies <caddy-binary>")
		os.Exit(2)
	}
	info, err := buildinfo.ReadFile(os.Args[1])
	if err == nil {
		err = checkDependencies(info)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dependency check failed:", err)
		os.Exit(1)
	}
	for _, dependency := range info.Deps {
		if dependency.Path == currentPlugin || dependency.Path == corazaModule {
			fmt.Printf("Verified %s %s\n", dependency.Path, dependency.Version)
		}
	}
}
