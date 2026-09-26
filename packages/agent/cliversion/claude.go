package cliversion

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Claude returns the Claude Code version installed on this machine, or "" until
// it is known. It never blocks. The first call starts a background refresh;
// later calls return the cached version. The shared disk cache limits probes
// across processes to once per ten minutes.
//
// Anthropic version-gates new models on the version terva's OAuth requests
// claim, and the wire's compiled baseline rots between releases, which is why
// the real install is worth finding (see provider.WithClaudeCodeVersion).
var Claude = newInstalledVersion("claude", runClaudeVersionProbe, parseClaudeVersion).get

// claudeVersionProbeTimeout bounds the background subprocess and the time
// it holds the shared cache lock.
const claudeVersionProbeTimeout = 3 * time.Second

// runClaudeVersionProbe executes the locally installed Claude Code CLI and
// returns its raw --version output. This runs a binary found on PATH — the
// same class of risk as terva running git. PATH is not project-controlled,
// and the output is only ever strictly parsed for a dotted version triple.
func runClaudeVersionProbe() (string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeVersionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// claudeVersionRE matches a dotted version triple, the shape inside Claude
// Code's "2.1.267 (Claude Code)" output. Anything that does not contain one
// is discarded rather than guessed at.
var claudeVersionRE = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

// parseClaudeVersion extracts the first dotted version triple from probe
// output. The bool reports whether one was found.
func parseClaudeVersion(out string) (string, bool) {
	m := claudeVersionRE.FindString(strings.TrimSpace(out))
	if m == "" {
		return "", false
	}
	return m, true
}
