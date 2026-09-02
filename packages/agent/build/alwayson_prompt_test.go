package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/testsupport"
)

// resolveWithPins builds a session in a throwaway home and repo carrying both
// an AGENTS.md and a --context-file, so all three neighbouring segments exist
// and the ordering assertion is not vacuous.
func resolveWithPins(t *testing.T, args Args) Resolved {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("repo rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctxFile := filepath.Join(repo, "extra.md")
	if err := os.WriteFile(ctxFile, []byte("extra context"), 0o644); err != nil {
		t.Fatal(err)
	}
	args.CWD = repo
	args.ContextFiles = []string{ctxFile}
	r, err := Resolve(args, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func segmentIndex(t *testing.T, r Resolved, source string) int {
	t.Helper()
	for i := range r.SystemSegments {
		if r.SystemSegments[i].Source == source {
			return i
		}
	}
	return -1
}

func segmentText(r Resolved, source string) string {
	for i := range r.SystemSegments {
		if r.SystemSegments[i].Source == source {
			return r.SystemSegments[i].Text
		}
	}
	return ""
}

// The placement decision, held by a test because prose in a comment does not
// fail a build.
//
// After the context files, before every AGENTS.md. That order is what lets a
// repository refine the pinned standard with a short delta instead of forking
// the whole body, and it reproduces the position that already worked when this
// standard lived in $TERVA_HOME/AGENTS.md.
func TestPinnedSkillBodyLandsAfterContextFilesAndBeforeAgentsMD(t *testing.T) {
	r := resolveWithPins(t, Args{})

	pinned := segmentIndex(t, r, SourcePinnedSkills)
	ctx := segmentIndex(t, r, SourceContextFiles)
	agents := segmentIndex(t, r, SourceAgentsMD)

	if pinned < 0 {
		t.Fatalf("no pinned-skills segment, but DefaultAlwaysOn names %v", skills.DefaultAlwaysOn)
	}
	if ctx < 0 || agents < 0 {
		t.Fatalf("fixture is missing a neighbour: context-files=%d agents-md=%d", ctx, agents)
	}
	if !(ctx < pinned && pinned < agents) {
		t.Errorf("order is context-files=%d pinned-skills=%d agents-md=%d, want ctx < pinned < agents",
			ctx, pinned, agents)
	}
}

// A pinned body is already in the prompt. Leaving its manifest line in place
// advertises text the model can already read and invites a wasted `skill` call
// to fetch it again.
func TestPinnedSkillLeavesTheManifest(t *testing.T) {
	r := resolveWithPins(t, Args{})
	name := skills.DefaultAlwaysOn[0]

	if body := segmentText(r, SourcePinnedSkills); !strings.Contains(body, name) {
		t.Fatalf("the pinned block does not mention %q", name)
	}
	manifest := segmentText(r, SourceSkills)
	if manifest == "" {
		t.Fatal("no skills manifest at all, so this test cannot tell exclusion from absence")
	}
	if strings.Contains(manifest, name+" [") {
		t.Errorf("%q still has a manifest line while its body is pinned:\n%s", name, manifest)
	}
}

// The per-session escape hatch. It suppresses pinning without touching skill
// discovery, so the skill returns to being loadable on demand and its manifest
// line comes back. That is what separates it from --no-skill.
func TestNoAlwaysOnSkillsDropsThePinAndRestoresTheManifestLine(t *testing.T) {
	r := resolveWithPins(t, Args{NoAlwaysOnSkills: true})
	name := skills.DefaultAlwaysOn[0]

	if i := segmentIndex(t, r, SourcePinnedSkills); i >= 0 {
		t.Errorf("--no-always-on-skills still pinned a body at segment %d", i)
	}
	if manifest := segmentText(r, SourceSkills); !strings.Contains(manifest, name+" [") {
		t.Errorf("%q did not return to the manifest:\n%s", name, manifest)
	}
}
