package build

import (
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// BuildPromptManifest assembles the source-of-truth manifest for the prompt
// that would be sent given a message history: the resolved system segments,
// the messages (a seeded card greeting labeled via Meta), the per-turn tail
// (triggered lore + PHI), and the tool list. It takes messages rather than an
// agent so a dump needs no live client/credential. Extension ephemeral context
// is produced live by the extension manager and is not captured here.
func (r *Resolved) BuildPromptManifest(msgs []provider.Message) PromptManifest {
	m := PromptManifest{Sections: []PromptSection{
		{Name: sectionSystem, Segments: r.SystemSegments},
		{Name: sectionMessages, Segments: messageSegments(msgs)},
		{Name: sectionTail, Segments: r.tailSegments(msgs)},
		{Name: sectionTools, Segments: toolManifestSegments(r.ToolRegistry, r.SystemSegments)},
	}}
	// Emit [] rather than null for empty sections so JSON consumers (jq
	// assertions) can always iterate .segments without a null guard.
	for i := range m.Sections {
		if m.Sections[i].Segments == nil {
			m.Sections[i].Segments = []PromptSegment{}
		}
	}
	return m
}

// messageSegments renders the transcript: one segment per message with its
// role and a source label (a seeded card greeting carries Meta["source"]).
func messageSegments(msgs []provider.Message) []PromptSegment {
	segs := make([]PromptSegment, 0, len(msgs))
	for _, m := range msgs {
		source := string(m.Role)
		if s := m.Meta["source"]; s != "" {
			source = s
		}
		segs = append(segs, PromptSegment{Source: source, Role: string(m.Role), Text: messageText(m)})
	}
	return segs
}

// messageText concatenates the visible text blocks of a message.
func messageText(m provider.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// tailSegments builds the uncached per-turn tail: the triggered-lore block
// (labeled with the entry files that fired) then a card's PHI — mirroring
// PerTurnContext, so the dump matches what would be injected.
func (r *Resolved) tailSegments(msgs []provider.Message) []PromptSegment {
	var segs []PromptSegment
	if len(r.loreTriggered) > 0 {
		fired := lore.Select(r.loreTriggered, r.loreConfig, recentLoreScan(msgs), lore.ApproxTokens).All()
		if block := lore.Render(fired); block != "" {
			// Framed exactly as tailProvider frames it: the dump's whole promise is
			// that it shows what would be injected, and the frame is injected.
			segs = append(segs, PromptSegment{Source: loreFiredLabel(fired), Text: loreReferenceFrame(block)})
		}
	}
	if r.postHistory != "" {
		segs = append(segs, PromptSegment{Source: SourceCardPostHistory, Text: r.postHistory})
	}
	return segs
}

func loreFiredLabel(entries []lore.Entry) string {
	if len(entries) == 0 {
		return "lore:triggered"
	}
	srcs := make([]string, 0, len(entries))
	for _, e := range entries {
		s := e.Source
		if s == "" {
			s = e.Name
		}
		srcs = append(srcs, s)
	}
	return "lore:triggered [" + strings.Join(srcs, ", ") + "]"
}

// toolManifestSegments lists the registry's tool names, plus the host-
// injected skins the live session would carry: the offline dump runs before
// injectHostTools / MergeExtensionTools, so the registry alone would show an
// EMPTY tool list while the system prompt tells the model to call actor_spawn
// (the cast addendum) or swarm_spawn (the auto-swarm addendum) — an
// internally inconsistent dump. The skins are reconstructed from the same
// system segments that advertise them, so the two can't drift; extension/MCP
// tools are runtime merges and are flagged as not shown.
func toolManifestSegments(reg core.Registry, system []PromptSegment) []PromptSegment {
	var segs []PromptSegment
	if len(reg) > 0 {
		names := make([]string, 0, len(reg))
		for name := range reg {
			names = append(names, name)
		}
		sort.Strings(names)
		segs = append(segs, PromptSegment{Source: "tools", Text: strings.Join(names, ", ")})
	}
	for _, s := range system {
		switch s.Source {
		case "cast":
			segs = append(segs, PromptSegment{Source: "tools:host", Text: "actor_spawn (host-injected at runtime; advertised by the cast addendum)"})
		case "auto-swarm":
			segs = append(segs, PromptSegment{Source: "tools:host", Text: "swarm_spawn (host-injected at runtime; advertised by the auto-swarm addendum)"})
		}
	}
	if len(segs) > 0 {
		segs = append(segs, PromptSegment{Source: "tools:note", Text: "extension/MCP tools merge at runtime and are not shown in an offline dump"})
	}
	return segs
}
