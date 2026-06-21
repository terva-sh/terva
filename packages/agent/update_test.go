package agent

import "testing"

func TestVersionLessHandlesPseudoVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // a < b
	}{
		// The regression: a Go pseudo-version dev build of the next patch
		// must NOT read as older than the release it was built after.
		{"0.108.3-0.20260620232809-089f7af04dc8", "0.108.2", false},
		{"0.108.2", "0.108.3-0.20260620232809-089f7af04dc8", true},
		// Ordinary releases.
		{"0.108.2", "0.108.3", true},
		{"0.108.3", "0.108.2", false},
		{"0.108.2", "0.108.2", false},
		{"0.9.9", "0.10.0", true},
		// Human "(commit, date)" and "+build" suffixes are ignored.
		{"0.108.2 (abc1234, 2026-04-18)", "0.108.3", true},
		{"0.108.3", "0.108.2 (abc1234, 2026-04-18)", false},
		{"0.108.3+meta", "0.108.2", false},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v; want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestBuildInfoDevBuildNotOfferedOlderRelease(t *testing.T) {
	// Exactly the report: on a 0.108.3 dev build, a published 0.108.2 must
	// NOT be offered as an "update".
	info := buildInfo("0.108.3-0.20260620232809-089f7af04dc8", "v0.108.2", "https://x")
	if info.Available {
		t.Fatalf("dev build offered an older release: Available=true (current=%q latest=%q)", info.Current, info.Latest)
	}
	if info.Latest != "0.108.2" {
		t.Errorf("Latest = %q; want the v-stripped 0.108.2", info.Latest)
	}
	// A genuinely newer release IS still offered to a released build.
	if got := buildInfo("0.108.2", "v0.108.3", "https://x"); !got.Available {
		t.Fatal("0.108.3 should be offered to a 0.108.2 build")
	}
}
