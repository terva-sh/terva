package build

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

	// Origin lists the files this segment's Text was read from, when it was
	// read from files at all — the AGENTS.md chain, the --context-file set.
	// Empty for everything terva generates rather than reads.
	//
	// It exists because the rendered Text embeds the paths as prose (`## /a/b`
	// headings) and prose is not a data structure. Two callers need the paths
	// as paths. A person debugging a 13KB agents-md segment wants to know WHICH
	// AGENTS.md files are in it, and the dump can now say so. And a foreign
	// worker must be POINTED at project context rather than pasted it — it has
	// its own file-reading tools and its own discovery, so a paste duplicates
	// or contradicts what it would find anyway (see PortabilityDiscoveryOwned).
	//
	// Recovering the paths by re-parsing the markdown was the alternative, and
	// it is exactly the fork the design forbids: a renderer that re-derives what
	// the assembler already knew is a second assembler with extra steps. The
	// assembler knew these paths and threw them away. Now it keeps them.
	Origin []string `json:"origin,omitempty"`
}

// PromptSection is one region of the assembled prompt.
type PromptSection struct {
	Name     string          `json:"name"` // system | messages | tail | tools
	Segments []PromptSegment `json:"segments"`
}

// Classified reports whether this section's segments carry a portability class
// — whether the segmentPortability table is about them at all.
//
// It is about system and tail: those are the regions terva ASSEMBLES from
// labeled sources, and the class answers "would this reach a foreign worker?".
// It is not about messages or tools. A user's turn is not a prompt segment
// terva composed; it is the conversation, and a briefing carries the task on
// purpose. Running it through PortabilityOf would print "harness-local" beside
// every message — the fail-closed default answering a question nobody asked,
// which reads as a finding and is not one.
func (s PromptSection) Classified() bool { return classifiedSection(s.Name) }

// classifiedSection is the section-name predicate behind Classified, shared
// with the sizes view so both renderings of a dump annotate exactly the same
// regions.
func classifiedSection(name string) bool {
	return name == sectionSystem || name == sectionTail
}

// MarshalJSON emits a classified section's segments with their portability
// class alongside the source.
//
// The class is DERIVED here, at the moment of rendering, exactly as
// PromptSegment.Portability() derives it — never stored on the segment. That is
// the whole discipline: a stored class can be set wrong at a construction site,
// forgotten at a new one, or drift from the label printed next to it. A derived
// one cannot. The dump therefore cannot lie about what would cross the harness
// boundary, which is the only reason to print it.
func (s PromptSection) MarshalJSON() ([]byte, error) {
	type segView struct {
		Source      string      `json:"source"`
		Portability Portability `json:"portability,omitempty"`
		Role        string      `json:"role,omitempty"`
		Origin      []string    `json:"origin,omitempty"`
		Text        string      `json:"text"`
	}
	views := make([]segView, 0, len(s.Segments))
	for _, seg := range s.Segments {
		v := segView{Source: seg.Source, Role: seg.Role, Origin: seg.Origin, Text: seg.Text}
		if s.Classified() {
			v.Portability = seg.Portability()
		}
		views = append(views, v)
	}
	return json.Marshal(struct {
		Name     string    `json:"name"`
		Segments []segView `json:"segments"`
	}{Name: s.Name, Segments: views})
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

// Text renders the manifest as annotated text: each section, then each segment
// prefixed with its [source] — plus, in the assembled regions, the portability
// class that says whether the segment would reach a foreign worker.
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
			if sec.Classified() {
				label += " · " + string(seg.Portability())
			}
			b.WriteString("---- [" + label + "] ----\n")
			// A file-backed segment names the files. "Why is agents-md 13KB?" is
			// answered by the next line rather than by reading 13KB.
			if len(seg.Origin) > 0 {
				b.WriteString("---- from: " + strings.Join(seg.Origin, ", ") + "\n")
			}
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
