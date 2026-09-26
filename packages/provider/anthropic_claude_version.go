package provider

import (
	"regexp"
	"strconv"
	"strings"
)

// Anthropic version-gates new models on the client version the OAuth path
// claims: a request from a version below a model's floor fails with http 400
// claude_code_version_too_old. The compiled claudeCodeVersion baseline rots
// between releases, so when the host knows of a real Claude Code install on
// this machine (WithClaudeCodeVersion) we claim its version instead, as long as
// it is newer than the baseline. That value is real by construction (it is the
// exact client this machine plausibly presents), and its refresh mechanism is
// `claude update`, the very command Anthropic's error message tells people to
// run.
//
// Finding the install means running it, which the wire does not do (decision
// 0021, rule 5). terva's harness probes it in packages/agent/cliversion.

// claimedClaudeCodeVersion is the version the OAuth user-agent claims: the
// host's installed version when it is newer than the baseline, the baseline
// otherwise.
func (c *anthropicClient) claimedClaudeCodeVersion() string {
	if c.host.claudeVersion == nil {
		return claudeCodeVersion
	}
	return pickClaudeCodeVersion(c.host.claudeVersion())
}

// claudeVersionRE matches a dotted version triple, the shape inside Claude
// Code's "2.1.267 (Claude Code)" output, which a host may pass as it is.
// Anything that does not contain one is discarded rather than guessed at.
var claudeVersionRE = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

// parseClaudeVersion extracts the first dotted version triple from what the
// host reported. The bool reports whether one was found.
func parseClaudeVersion(out string) (string, bool) {
	m := claudeVersionRE.FindString(strings.TrimSpace(out))
	if m == "" {
		return "", false
	}
	return m, true
}

// pickClaudeCodeVersion decides what version to claim given the host's
// installed version: that version when it parses and is newer than the
// compiled baseline, the baseline otherwise.
func pickClaudeCodeVersion(probeOutput string) string {
	probed, ok := parseClaudeVersion(probeOutput)
	if !ok {
		return claudeCodeVersion
	}
	if versionTripleNewer(probed, claudeCodeVersion) {
		return probed
	}
	return claudeCodeVersion
}

// versionTripleNewer reports whether a is strictly newer than b. Both
// arguments must be dotted version triples; the comparison is numeric per
// part, so 2.1.67 does not beat 2.1.267 lexically.
func versionTripleNewer(a, b string) bool {
	as := strings.SplitN(a, ".", 3)
	bs := strings.SplitN(b, ".", 3)
	for i := 0; i < 3; i++ {
		an, _ := strconv.Atoi(as[i])
		bn, _ := strconv.Atoi(bs[i])
		if an != bn {
			return an > bn
		}
	}
	return false
}
