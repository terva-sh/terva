package agent

import (
	"encoding/json"
	"strings"
)

// PromptSegment is one labeled piece of an assembled prompt: its provenance
// (Source), an optional message Role, and the Text as it appears in the
// prompt. Segments are the source of truth — the flat strings actually sent
// (the system prompt and the per-turn tail) are rendered by joining segment
// texts, so a segment's label can never drift from what the model receives.
type PromptSegment struct {
	Source string `json:"source"`
	Role   string `json:"role,omitempty"`
	Text   string `json:"text"`
}

// PromptSection is one region of the assembled prompt.
type PromptSection struct {
	Name     string          `json:"name"` // system | messages | tail | tools
	Segments []PromptSegment `json:"segments"`
}

// PromptManifest is the structured, source-of-truth view of an assembled
// prompt. It renders three ways: Text (annotated, [source]-labeled — the
// "where did this come from" view), JSON (for assertions/tooling), and Raw
// (verbatim segment texts, unlabeled — the "what actually goes out" view; the
// logical prompt, NOT the literal wire payload).
type PromptManifest struct {
	Sections []PromptSection `json:"sections"`
}

const (
	sectionSystem   = "system"
	sectionMessages = "messages"
	sectionTail     = "tail"
	sectionTools    = "tools"
)

func (m PromptManifest) section(name string) *PromptSection {
	for i := range m.Sections {
		if m.Sections[i].Name == name {
			return &m.Sections[i]
		}
	}
	return nil
}

// Text renders the manifest as annotated text: each section, then each
// segment prefixed with its [source] (and role, for messages).
func (m PromptManifest) Text() string {
	var b strings.Builder
	for si, sec := range m.Sections {
		if si > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("==== " + strings.ToUpper(sec.Name) + " ====\n")
		if len(sec.Segments) == 0 {
			b.WriteString("(empty)\n")
			continue
		}
		for _, seg := range sec.Segments {
			label := seg.Source
			if seg.Role != "" {
				label = seg.Role + " · " + label
			}
			b.WriteString("---- [" + label + "] ----\n")
			b.WriteString(seg.Text)
			if !strings.HasSuffix(seg.Text, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// JSON renders the manifest as indented JSON.
func (m PromptManifest) JSON() string {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(out)
}

// Raw renders the verbatim prompt: segment texts only, no labels. Sections
// and segments are separated by blank lines; empty sections are skipped
// entirely — Text() marks them "(empty)", but here a separator with nothing
// after it would just leave stray gaps (mid-document or trailing). This is
// the logical prompt, not the literal wire payload (no cache markers / JSON
// escaping).
func (m PromptManifest) Raw() string {
	var b strings.Builder
	wrote := false
	for _, sec := range m.Sections {
		if len(sec.Segments) == 0 {
			continue
		}
		if wrote {
			b.WriteString("\n\n")
		}
		for gi, seg := range sec.Segments {
			if gi > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(seg.Text)
		}
		wrote = true
	}
	return b.String()
}
