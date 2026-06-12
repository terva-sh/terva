package main

import (
	"runtime/debug"
	"testing"
)

func TestBuildInfoVersion(t *testing.T) {
	cases := []struct {
		name    string
		bi      *debug.BuildInfo
		v, c, d string
	}{
		{name: "nil info", bi: nil},
		{
			name: "go install of a tagged version",
			bi:   &debug.BuildInfo{Main: debug.Module{Version: "v0.104.2"}},
			v:    "0.104.2",
		},
		{
			name: "devel module version is ignored",
			bi:   &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
		},
		{
			name: "source build with vcs info",
			bi: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abcdef1234567890"},
					{Key: "vcs.time", Value: "2026-06-11T00:00:00Z"},
				},
			},
			c: "abcdef1234567890",
			d: "2026-06-11T00:00:00Z",
		},
		{
			name: "pseudo-version from @main",
			bi:   &debug.BuildInfo{Main: debug.Module{Version: "v0.104.2-0.20260611000000-abcdef123456"}},
			v:    "0.104.2-0.20260611000000-abcdef123456",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, c, d := buildInfoVersion(tc.bi)
			if v != tc.v || c != tc.c || d != tc.d {
				t.Fatalf("buildInfoVersion = (%q, %q, %q), want (%q, %q, %q)", v, c, d, tc.v, tc.c, tc.d)
			}
		})
	}
}
