package build

import (
	"strings"
	"testing"
)

// The charter is the one system segment whose text a user chose by picking a
// file, and until now the dump could not say which file. These tests pin the
// provenance, because the failure it exists to prevent is silent: a persona
// that does not carry a behaviour reads exactly like one that does.

func charterSegment(t *testing.T, segs []PromptSegment) PromptSegment {
	t.Helper()
	var found []PromptSegment
	for _, s := range segs {
		if s.Source == SourceCharter {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 charter segment, got %d", len(found))
	}
	return found[0]
}

func TestCharterSegmentCarriesItsPersonaFile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
	}{
		{"builtin", "embedded:mieli.md"},
		{"on-disk", "/home/someone/.terva/personas/assistant.md"},
		{"extension", "ext:websearch:deep-researcher.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seg := charterSegment(t, SystemSegments(SystemPromptOpts{
				Charter:       "Work carefully.",
				CharterOrigin: tc.source,
			}))
			if len(seg.Origin) != 1 || seg.Origin[0] != tc.source {
				t.Errorf("charter Origin = %v, want [%s]", seg.Origin, tc.source)
			}
		})
	}
}

// The legacy name-only persona swap (TERVA_PERSONA_NAME) has no file behind it.
// An Origin pointing at nothing is worse than no Origin: the dump would print a
// "from:" line naming the empty string.
func TestCharterOriginIsEmptyWhenNoFileBackedPersona(t *testing.T) {
	seg := charterSegment(t, SystemSegments(SystemPromptOpts{Charter: "Work carefully."}))
	if len(seg.Origin) != 0 {
		t.Errorf("charter Origin = %v, want none for a persona with no source file", seg.Origin)
	}
}

// Provenance must not change a single byte of what the model is sent. The
// charter segment gained a field, not a rendering.
func TestCharterOriginDoesNotChangeThePrompt(t *testing.T) {
	opts := SystemPromptOpts{Charter: "Work carefully.", PersonaName: "Vartija"}
	bare := BuildSystemPrompt(opts)
	opts.CharterOrigin = "embedded:vartija.md"
	if withOrigin := BuildSystemPrompt(opts); withOrigin != bare {
		t.Errorf("Origin changed the prompt text:\n--- without ---\n%s\n--- with ---\n%s", bare, withOrigin)
	}
}

// The dump is the whole point: a person asking "where did this instruction come
// from" must get the answer without reading Go.
func TestPromptDumpNamesTheCharterFile(t *testing.T) {
	m := PromptManifest{Sections: []PromptSection{{
		Name: "system",
		Segments: SystemSegments(SystemPromptOpts{
			Charter:       "Work carefully.",
			CharterOrigin: "embedded:mieli.md",
		}),
	}}}
	if got := m.Text(); !strings.Contains(got, "from: embedded:mieli.md") {
		t.Errorf("dump does not name the charter's file:\n%s", got)
	}
}

// A composed charter is two segments under one label, each keeping its own
// origin — the only way "where did this instruction come from" stays answerable
// once a charter has two authors.
func TestComposedCharterKeepsBothOrigins(t *testing.T) {
	segs := SystemSegments(SystemPromptOpts{
		BaseCharter:       "Inspect before changing.",
		BaseCharterOrigin: "embedded:mieli.md",
		Charter:           "Track what is in flight.",
		CharterOrigin:     "/personas/assistant.md",
	})
	var charters []PromptSegment
	for _, s := range segs {
		if s.Source == SourceCharter {
			charters = append(charters, s)
		}
	}
	if len(charters) != 2 {
		t.Fatalf("want 2 charter segments, got %d", len(charters))
	}
	if charters[0].Text != "Inspect before changing." {
		t.Errorf("the inherited charter must come first; got %q", charters[0].Text)
	}
	if len(charters[0].Origin) != 1 || charters[0].Origin[0] != "embedded:mieli.md" {
		t.Errorf("base origin = %v", charters[0].Origin)
	}
	if len(charters[1].Origin) != 1 || charters[1].Origin[0] != "/personas/assistant.md" {
		t.Errorf("own origin = %v", charters[1].Origin)
	}
}

// Splitting the segment must not change what the model reads. Two segments join
// on a blank line, which is exactly how ComposedCharter joins the same halves.
func TestComposedSegmentsMatchTheComposedCharterText(t *testing.T) {
	p := Persona{
		Charter:   "Track what is in flight.",
		Inherited: "Inspect before changing.",
	}
	sys := BuildSystemPrompt(SystemPromptOpts{
		BaseCharter: p.Inherited,
		Charter:     p.Charter,
	})
	if !strings.Contains(sys, p.ComposedCharter()) {
		t.Errorf("prompt does not contain the composed charter verbatim:\nwant %q\nin\n%s", p.ComposedCharter(), sys)
	}
}
