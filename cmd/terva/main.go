// Command terva is an agent harness — a coding agent out of the box,
// extensible in any language to operate any tools — shipped as a single
// static Go binary.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"terva.sh/terva/packages/agent"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/buildinfo"
)

// Injected at build time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
// See .goreleaser.yaml for the release build and the justfile (`just
// build`) for local builds. Binaries built without ldflags (`go install
// terva.sh/terva/cmd/terva@vX.Y.Z`, plain `go build`) recover what
// they can from the embedded module build info instead.
var (
	// 0.0.0 is a SENTINEL meaning "no version was stamped", not a
	// version anything reports: main() below treats it as absent and
	// falls through to the module build info. The justfile stamps it
	// on EVERY local build, so a local binary's version always comes
	// from build info in practice — a pseudo-version like
	// 0.130.2-0.<ts>-<commit>, which reads "a dev build after
	// v0.130.1" and does NOT mean 0.130.2 was cut.
	version = "0.0.0"
	commit  = ""
	date    = ""
)

// buildInfoVersion recovers version/commit/date from the module build
// info for binaries built without ldflags: `go install ...@vX.Y.Z`
// stamps the module version, and VCS builds carry vcs.revision and
// vcs.time. Empty results mean the info isn't there (e.g. a (devel)
// module version), and the ldflags defaults stand.
//
// ⚠️ The commit Go derives here is NOT reliably the commit being built.
// Go resolves VCS state from the repository, and from a git worktree it
// reports the PRIMARY checkout's HEAD — so a worktree build's recovered
// revision (and the hash embedded in a pseudo-version built from it)
// names a tree you did not build. Only the ldflag-stamped commit is
// trustworthy, which is why it wins whenever it is set: this fallback
// fills in `commit` only when nothing stamped one.
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
	// Before anything touches credentials: core dumps off (the
	// process will hold auth.json contents in memory). Lives in main,
	// not agent.Run, so SDK embedders keep their own rlimit policy.
	procenv.Harden()
	// Optional pprof endpoint, compiled in only under the `terva_pprof`
	// build tag (see pprof.go / `just install-dev`); a no-op in every
	// other build, so release binaries never carry the profiling
	// handlers. Even when present it stays off until TERVA_PPROF is set.
	maybeStartPprof()
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
	// Record the structured triple before it's folded into the combined
	// display string — terva_status and the relaunch/startup version
	// lines read it back to report the running process's build (see
	// packages/buildinfo).
	info := buildinfo.Info{Version: version, Commit: commit, Date: date}
	buildinfo.Set(info)
	if err := agent.Run(os.Args[1:], info.String()); err != nil {
		fmt.Fprintln(os.Stderr, "terva:", err)
		os.Exit(1)
	}
}
