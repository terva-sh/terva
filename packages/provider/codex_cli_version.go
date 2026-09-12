package provider

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// codexCLIVersion is the compiled baseline, and the floor: the version claimed
// when no local Codex CLI install answers the probe.
//
// Verified against `codex --version` on 2026-09-11, which prints
// "codex-cli 0.153.4".
const codexCLIVersion = "0.153.4"

// effectiveCodexCLIVersion is the Codex CLI version the native identity claims.
//
// This mirrors effectiveClaudeCodeVersion deliberately, because the operator
// asked for the same treatment, but the two exist for different reasons and a
// later reader should not infer a gate that nobody has observed. Anthropic
// version-gates models and returns http 400 claude_code_version_too_old, so
// there the version is an ADMISSION requirement. Here nothing is known to
// check it: the header decomposition in codex_identity_ab_test.go sent a plain
// "codex_cli_rs/0.0.0" and the backend served it. So the version is here to
// make a claimed identity coherent, and for nothing else.
//
// Floor, never ceiling. Anything unexpected collapses to the baseline: no
// binary on PATH, a hung or failing probe, unparseable output, or an install
// older than the baseline.
//
// The probe runs at most once per process, and only when an operator has
// switched the native identity on, so the default path never pays the exec.
// See ticket TKT-01M29FMAXGQYQD79VYSGWYAH4N.
var effectiveCodexCLIVersion = sync.OnceValue(func() string {
	out, err := runCodexVersionProbe()
	if err != nil {
		return codexCLIVersion
	}
	return pickCodexCLIVersion(out)
})

// codexVersionProbeTimeout bounds the probe so a hung `codex` process cannot
// stall the first request. The fallback on timeout is the baseline, so a tight
// bound costs little.
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

// pickCodexCLIVersion decides what version to claim given raw probe output:
// the probed version when it parses and is newer than the compiled baseline,
// the baseline otherwise.
func pickCodexCLIVersion(probeOutput string) string {
	probed, ok := parseCodexVersion(probeOutput)
	if !ok {
		return codexCLIVersion
	}
	if versionTripleNewer(probed, codexCLIVersion) {
		return probed
	}
	return codexCLIVersion
}
