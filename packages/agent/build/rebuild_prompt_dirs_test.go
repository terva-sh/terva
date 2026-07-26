package build

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A post-resolve rebuild (extra tools injected, extension/MCP tools merged)
// must re-render from what Resolve captured, not re-derive its inputs. The
// re-deriving rebuild never carried TervaExamplesDir — so the examples hint
// silently vanished from the system prompt in any session whose host injects
// tools — and it hardcoded the docs dir, resurrecting a hint that a failed
// EnsureInstalled had correctly suppressed.

func segSources(segs []PromptSegment) map[string]int {
	out := map[string]int{}
	for _, s := range segs {
		out[s.Source]++
	}
	return out
}

func TestRebuildSystemPromptKeepsResolvedPromptDirs(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	r, err := Resolve(Args{CWD: testsupport.TempDir(t)}, false)
	if err != nil {
		t.Fatal(err)
	}

	before := r.SystemSegments
	src := segSources(before)
	if src[SourceTervaDocsHint] != 1 || src[SourceTervaExamplesHint] != 1 {
		t.Fatalf("resolve should render both doc hints (EnsureInstalled populates the scratch home); sources: %v", src)
	}

	// An input-less rebuild must be a fixed point: same segments, same text.
	r.rebuildSystemPrompt()
	after := r.SystemSegments
	if len(after) != len(before) {
		t.Fatalf("rebuild changed the segment set: before %v, after %v", src, segSources(after))
	}
	for i := range before {
		if before[i].Source != after[i].Source {
			t.Fatalf("segment %d source changed across rebuild: %q → %q", i, before[i].Source, after[i].Source)
		}
		// The footer re-renders the current date; everything else must be
		// byte-identical.
		if before[i].Source == SourceFooter {
			continue
		}
		if before[i].Text != after[i].Text {
			t.Errorf("segment %q text changed across an input-less rebuild:\nbefore: %q\nafter:  %q",
				before[i].Source, before[i].Text, after[i].Text)
		}
	}

	// When EnsureInstalled fails, Resolve renders no hints — and a rebuild
	// must not resurrect one from a re-derived path the install never wrote.
	r.tervaDocsDir, r.tervaExamplesDir = "", ""
	r.rebuildSystemPrompt()
	src = segSources(r.SystemSegments)
	if src[SourceTervaDocsHint] != 0 || src[SourceTervaExamplesHint] != 0 {
		t.Errorf("rebuild resurrected a doc hint resolve had suppressed; sources: %v", src)
	}
}
