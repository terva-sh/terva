package provider

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Anthropic version-gates new models on the client version the OAuth path
// claims: a request from a version below a model's floor fails with http 400
// claude_code_version_too_old. The compiled claudeCodeVersion baseline rots
// between releases, so when a real Claude Code install is present on this
// machine we claim its version instead, as long as it is newer than the
// baseline. That value is real by construction (it is the exact client this
// machine plausibly presents), and its refresh mechanism is `claude update`,
// the very command Anthropic's error message tells people to run.
//
// The probe runs `claude --version` at most once per process, lazily on the
// first OAuth-path request, so API-key users and other providers never pay
// the exec. Anything unexpected — no binary on PATH, a hung or failing
// probe, unparseable output, an older install — collapses to the compiled
// baseline. Floor, never ceiling. See ticket TKT-01M24CWCV43.
var effectiveClaudeCodeVersion = sync.OnceValue(func() string {
	out, err := runClaudeVersionProbe()
	if err != nil {
		return claudeCodeVersion
	}
	return pickClaudeCodeVersion(out)
})

// claudeVersionProbeTimeout bounds the probe so a hung `claude` process
// cannot stall the first request. The binary is a Node program: a warm start
// answers in well under a second, and the fallback on timeout is the
// baseline, so a tight bound costs little.
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

// pickClaudeCodeVersion decides what version to claim given raw probe
// output: the probed version when it parses and is newer than the compiled
// baseline, the baseline otherwise.
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
