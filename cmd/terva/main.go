// Command terva is a lightweight terminal coding agent.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"terva.sh/terva/packages/agent"
)

// Injected at build time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
// See .goreleaser.yaml for the release build and the Makefile for
// local builds. Binaries built without ldflags (`go install
// terva.sh/terva/cmd/terva@vX.Y.Z`, plain `go build`) recover what
// they can from the embedded module build info instead.
var (
	// 0.0.0 is the pre-release placeholder for local / untagged
	// builds; anything real overrides it via ldflags or build info.
	version = "0.0.0"
	commit  = ""
	date    = ""
)

// buildInfoVersion recovers version/commit/date from the module build
// info for binaries built without ldflags: `go install ...@vX.Y.Z`
// stamps the module version, and VCS builds carry vcs.revision and
// vcs.time. Empty results mean the info isn't there (e.g. a (devel)
// module version), and the ldflags defaults stand.
func buildInfoVersion(bi *debug.BuildInfo) (v, c, d string) {
	if bi == nil {
		return "", "", ""
	}
	if mv := bi.Main.Version; mv != "" && mv != "(devel)" {
		v = strings.TrimPrefix(mv, "v")
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			c = s.Value
		case "vcs.time":
			d = s.Value
		}
	}
	return v, c, d
}

func main() {
	if version == "0.0.0" || commit == "" || date == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			bv, bc, bd := buildInfoVersion(bi)
			if version == "0.0.0" && bv != "" {
				version = bv
			}
			if commit == "" {
				commit = bc
			}
			if date == "" {
				date = bd
			}
		}
	}
	v := version
	if commit != "" {
		short := commit
		if len(short) > 7 {
			short = short[:7]
		}
		v = v + " (" + short
		if date != "" {
			v = v + ", " + date
		}
		v = v + ")"
	}
	if err := agent.Run(os.Args[1:], v); err != nil {
		fmt.Fprintln(os.Stderr, "terva:", err)
		os.Exit(1)
	}
}
