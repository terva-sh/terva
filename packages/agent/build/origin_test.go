package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// Origin is a promise about the segment beside it: these are the files that
// block was built from. The promise is only worth making if it cannot drift, so
// pin the invariant directly — every path Origin claims must actually appear in
// the text, and every file the text names must appear in Origin. A renderer
// that pointed a worker at a file the block did not contain (or omitted one it
// did) would be worse than no pointer at all.
func TestOriginAgreesWithTheTextItSummarises(t *testing.T) {
	home := testsupport.TempDir(t)
	repo := testsupport.TempDir(t)
	nested := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(home, "AGENTS.md"):   "global rule",
		filepath.Join(repo, "AGENTS.md"):   "repo rule",
		filepath.Join(nested, "AGENTS.md"): "package rule",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	text, origin := readAgentsContext(nested, home)
	if len(origin) == 0 {
		t.Fatal("three AGENTS.md files exist; origin is empty")
	}
	for _, p := range origin {
		if !strings.Contains(text, p) {
			t.Errorf("origin claims %q but the block never mentions it — a worker would be pointed at a file that is not in its context", p)
		}
	}
	// The block writes each file as a `## <path>` heading. Every one of those
	// must be accounted for, or Origin is under-reporting what it carries.
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		named := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		found := false
		for _, p := range origin {
			if p == named {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the block contains %q but origin does not list it", named)
		}
	}
}

// The whole reason Origin exists: a discovery-owned segment must be POINTABLE.
// If Resolve emits one of these with no paths attached, the composer has nothing
// to point at and the only options left are to paste the payload (duplicating the
// worker's own discovery) or drop the context silently. Both are the bug.
func TestDiscoveryOwnedSegmentsCarryTheirPaths(t *testing.T) {
	repo := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("repo rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctxFile := filepath.Join(repo, "extra.md")
	if err := os.WriteFile(ctxFile, []byte("extra context"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{CWD: repo, ContextFiles: []string{ctxFile}}, false)
	if err != nil {
		t.Fatal(err)
	}

	// agents-md and context-files are the two a worker can be pointed at. (The
	// skills manifest is discovery-owned too but lists skill NAMES, not files —
	// and terva's skills are not a foreign agent's skills, so there is nothing
	// honest to point at. It carries no Origin on purpose.)
	for _, src := range []string{SourceAgentsMD, SourceContextFiles} {
		var seg *PromptSegment
		for i := range r.SystemSegments {
			if r.SystemSegments[i].Source == src {
				seg = &r.SystemSegments[i]
			}
		}
		if seg == nil {
			t.Fatalf("expected a %s segment in a repo with AGENTS.md and a --context-file", src)
		}
		if seg.Portability() != PortabilityDiscoveryOwned {
			t.Fatalf("%s should be discovery-owned, got %s", src, seg.Portability())
		}
		if len(seg.Origin) == 0 {
			t.Errorf("%s is discovery-owned but carries no Origin — there is nothing to point a worker at", src)
		}
	}
}
