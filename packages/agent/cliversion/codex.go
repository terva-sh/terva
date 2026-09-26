package cliversion

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Codex returns the Codex CLI version installed on this machine, or "" until it
// is known. It never blocks, and it probes only once something asks, which the
// codex client does only when native identity is on (see
// provider.WithCodexCLIVersion). A shared ten-minute cache limits probes across
// processes. See ticket TKT-01M29FMAXGQYQD79VYSGWYAH4N.
var Codex = newInstalledVersion("codex", runCodexVersionProbe, parseCodexVersion).get

// codexVersionProbeTimeout bounds the background subprocess and the time
// it holds the shared cache lock.
const codexVersionProbeTimeout = 3 * time.Second

// runCodexVersionProbe executes the locally installed Codex CLI and returns
// its raw --version output. This runs a binary found on PATH, the same class
// of risk as terva running git. PATH is not project-controlled, and the output
// is only ever strictly parsed for a dotted version triple.
func runCodexVersionProbe() (string, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexVersionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// codexVersionRE matches a dotted version triple, the shape inside the Codex
// CLI's "codex-cli 0.153.4" output. Anything without one is discarded rather
// than guessed at.
//
// Not shared with claudeVersionRE on purpose. The two patterns agree today by
// coincidence, and one shared pattern would couple two unrelated vendors'
// output formats, so a change to either CLI's text would arrive as a puzzle in
// the other's probe.
var codexVersionRE = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

// parseCodexVersion extracts the first dotted version triple from probe
// output. The bool reports whether one was found.
func parseCodexVersion(out string) (string, bool) {
	m := codexVersionRE.FindString(strings.TrimSpace(out))
	if m == "" {
		return "", false
	}
	return m, true
}
