package main

import (
	"runtime/debug"
	"testing"
)

func TestSecurityVersionFloor(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{
		{"v3.0.0-20230117071831-8b909c7fc345", false},
		{"v3.0.1", false},
		{"v3.3.2", false},
		{"v3.3.3-rc.1", false},
		{"v3.3.3", true},
		{"v3.8.0", true},
		{"v3.10.0", true},
		{"v3.3.3+build.1", true},
		{"(devel)", false},
		{"v3.3", false},
		{"v3.03.3", false},
		{"v3.+3.3", false},
	} {
		t.Run(test.version, func(t *testing.T) {
			if got := atLeast(test.version, minimumCoraza); got != test.want {
				t.Fatalf("atLeast(%q, %q) = %v; want %v", test.version, minimumCoraza, got, test.want)
			}
		})
	}
}

func TestBinaryDependencies(t *testing.T) {
	goodPlugin := &debug.Module{Path: currentPlugin, Version: "v2.6.1"}
	goodCore := &debug.Module{Path: corazaModule, Version: "v3.8.0"}
	for _, test := range []struct {
		name      string
		deps      []*debug.Module
		wantError bool
	}{
		{"fixed", []*debug.Module{goodPlugin, goodCore}, false},
		{"missing core", []*debug.Module{goodPlugin}, true},
		{"missing plugin", []*debug.Module{goodCore}, true},
		{"legacy plugin", []*debug.Module{goodPlugin, goodCore, {Path: legacyPlugin, Version: "v1.2.2"}}, true},
		{"vulnerable core", []*debug.Module{goodPlugin, {Path: corazaModule, Version: "v3.3.2"}}, true},
		{"local replacement", []*debug.Module{goodPlugin, {Path: corazaModule, Version: "v3.8.0", Replace: &debug.Module{Path: "../coraza"}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkDependencies(&debug.BuildInfo{Deps: test.deps})
			if (err != nil) != test.wantError {
				t.Fatalf("checkDependencies() = %v; wantError %v", err, test.wantError)
			}
		})
	}
}
