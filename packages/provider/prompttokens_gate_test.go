package provider

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// promptSumRe matches a hand-written prompt sum: InputTokens added to
// CacheReadTokens, on one line, however the receivers are spelled.
//
// Anchored on that PAIR because it is the pair that defines the sum. A line
// adding InputTokens to something else (session_inspect's
// Input+CacheRead+Output "did anything happen at all" check) is a different
// quantity and is not matched — it does not name a prompt.
var promptSumRe = regexp.MustCompile(`\.InputTokens\s*\+\s*[A-Za-z0-9_.]*\.?CacheReadTokens`)

// exemptFromPromptTokens are the lines allowed to spell the sum out, by
// file, with the count they are allowed and why.
var exemptFromPromptTokens = map[string]struct {
	reason  string
	allowed int
}{
	// The definition itself.
	filepath.Join("packages", "provider", "provider.go"): {"PromptTokens IS this sum", 1},

	// Not a prompt: a "did this turn reach the model at all" check that adds
	// output tokens in, so it is neither the gauge nor a ratio denominator.
	filepath.Join("packages", "agent", "tools", "session_inspect.go"): {"Input+CacheRead+Output is a did-anything-happen check, not a prompt", 1},
}

// Nobody spells the prompt sum by hand.
//
// Usage.PromptTokens is "everything the model read: the uncached remainder plus
// whatever came from, or went into, the cache", and Usage.CacheHitRate is the
// share of THAT served from cache. Both had one production caller each while
// twenty sites spelled the arithmetic out, and five of those dropped
// CacheWriteTokens — which is prompt the model read, just billed differently.
//
// What the omission cost: `terva replay` on an Anthropic session whose first
// turn wrote a 100k prefix to cache seeded its context gauge from
// Input+CacheRead and showed ~0 where the live gauge showed 100,000, then
// carried the gap through every later frame. In the same session
// `session_inspect stats` printed a cache hit rate over Input+CacheRead while
// the /context pane printed one over the whole prompt — two different
// percentages for one session, both labelled "cache hit rate".
//
// Scanned rather than listed: a list of the twenty known sites could not fail
// when a twenty-first was added, which is the only moment it needed to.
func TestNobodySpellsThePromptSumByHand(t *testing.T) {
	root := filepath.Join("..", "..")
	scanned := 0
	seen, visited := map[string]int{}, map[string]bool{}
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if testsupport.SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		if _, ok := exemptFromPromptTokens[rel]; ok {
			visited[rel] = true
		}
		for i, line := range strings.Split(string(data), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if !promptSumRe.MatchString(code) {
				continue
			}
			seen[rel]++
			if ex, ok := exemptFromPromptTokens[rel]; ok && seen[rel] <= ex.allowed {
				continue
			}
			offenders = append(offenders, rel+":"+itoa(i+1)+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d non-test Go files; the walk is broken and a pass here proves nothing", scanned)
	}
	for _, o := range offenders {
		t.Errorf("%s\n  use Usage.PromptTokens() / Usage.CacheHitRate(). A hand-written sum is how five sites "+
			"came to omit CacheWriteTokens and report a context gauge and a hit rate that disagreed with "+
			"the live ones for the same session.", o)
	}
	for rel, ex := range exemptFromPromptTokens {
		if !visited[rel] {
			t.Errorf("exemptFromPromptTokens names %s, which the scan never reached — it moved or was deleted", rel)
			continue
		}
		if seen[rel] != ex.allowed {
			t.Errorf("exemptFromPromptTokens allows %d line(s) in %s (%s) but the file has %d; "+
				"lower the count so the spare licence cannot shelter a new one", ex.allowed, rel, ex.reason, seen[rel])
		}
	}
}

// A clean report and a walk that visited nothing read identically. Plant one,
// assert the scan finds it; the regexp is the whole gate, so it is what gets
// exercised.
func TestThePromptSumScanActuallyMatches(t *testing.T) {
	for _, bad := range []string{
		"c.ctxTokens = r.Usage.InputTokens + r.Usage.CacheReadTokens",
		"used := last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens",
		"total:=u.InputTokens+u.CacheReadTokens",
	} {
		if !promptSumRe.MatchString(bad) {
			t.Errorf("the scan does not match a hand-written sum: %q", bad)
		}
	}
	for _, ok := range []string{
		"return u.PromptTokens()",
		"fmt.Fprintf(&b, \"%d\", usage.InputTokens)",
		"x := u.CacheReadTokens + u.CacheWriteTokens", // a cache total, not a prompt
	} {
		if promptSumRe.MatchString(ok) {
			t.Errorf("the scan matches something that is not a hand-written prompt sum: %q", ok)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
