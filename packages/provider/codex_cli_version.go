package provider

import (
	"regexp"
	"strings"
)

// codexCLIVersion is the compiled baseline, and the floor: the version claimed
// when no local Codex CLI install answers the probe.
//
// Verified against `codex --version` on 2026-09-11, which prints
// "codex-cli 0.153.4".
const codexCLIVersion = "0.153.4"

// claimedCodexCLIVersion is the Codex CLI version the native identity claims:
// the host's installed version (WithCodexCLIVersion) when it is newer than the
// baseline, the baseline otherwise.
//
// This mirrors claimedClaudeCodeVersion deliberately, because the operator
// asked for the same treatment, but the two exist for different reasons and a
// later reader should not infer a gate that nobody has observed. Anthropic
// version-gates models and returns http 400 claude_code_version_too_old, so
// there the version is an ADMISSION requirement. Here nothing is known to
// check it: the header decomposition in codex_identity_ab_test.go sent a plain
// "codex_cli_rs/0.0.0" and the backend served it. So the version is here to
// make a claimed identity coherent, and for nothing else.
//
// It is asked for only when native identity is on, so a host that probes on
// first call (terva's harness, packages/agent/cliversion) probes only then.
// See ticket TKT-01M29FMAXGQYQD79VYSGWYAH4N.
func (c *codexClient) claimedCodexCLIVersion() string {
	if c.cliVersion == nil {
		return codexCLIVersion
	}
	return pickCodexCLIVersion(c.cliVersion())
}

// WithCodexCLIVersion tells the codex client which Codex CLI version is
// installed on this machine, for the native identity's user-agent. It must not
// block: return "" until the version is known.
//
// Unstable: a client identity knob carries no promise before 1.0.
func WithCodexCLIVersion(installed func() string) CodexOption {
	return func(c *codexClient) { c.cliVersion = installed }
}

// codexVersionRE matches a dotted version triple, the shape inside the Codex
// CLI's "codex-cli 0.153.4" output, which a host may pass as it is. Anything
// without one is discarded rather than guessed at.
//
// Not shared with claudeVersionRE on purpose. The two patterns agree today by
// coincidence, and one shared pattern would couple two unrelated vendors'
// output formats, so a change to either CLI's text would arrive as a puzzle in
// the other's probe.
var codexVersionRE = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

// parseCodexVersion extracts the first dotted version triple from what the
// host reported. The bool reports whether one was found.
func parseCodexVersion(out string) (string, bool) {
	m := codexVersionRE.FindString(strings.TrimSpace(out))
	if m == "" {
		return "", false
	}
	return m, true
}

// pickCodexCLIVersion decides what version to claim given the host's installed
// version: that version when it parses and is newer than the compiled
// baseline, the baseline otherwise.
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
