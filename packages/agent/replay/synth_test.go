package replay

import (
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func msg(role provider.Role, blocks ...provider.Content) provider.Message {
	return provider.Message{Role: role, Content: blocks}
}

func eventTypes(frames []Frame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = f.Event.Type()
	}
	return out
}

// collapse consecutive duplicate types (text is chunked into N deltas).
func collapse(in []string) []string {
	var out []string
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// TestSynthesizeTurnOrdering pins the exact live-turn event order for a
// tool-using prompt: a first turn that calls a tool, its result, then a
// terminal text turn closed by EvDone.
func TestSynthesizeTurnOrdering(t *testing.T) {
	rows := []core.ReplayRow{
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleUser, provider.TextBlock{Text: "hi"})},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleAssistant,
			provider.TextBlock{Text: "let me look"},
			provider.ToolCallBlock{ID: "t1", Name: "read", Arguments: []byte(`{"p":"x"}`)},
		)},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleTool,
			provider.ToolResultBlock{CallID: "t1", Content: []provider.Content{provider.TextBlock{Text: "data"}}},
		)},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleAssistant, provider.TextBlock{Text: "all done"})},
	}
	got := collapse(eventTypes(Synthesize(rows, Options{Mode: ModeRaw})))
	want := []string{
		"user_message",
		"turn_start", "assistant_start", "text_delta",
		"tool_use_start", "tool_use_args", "tool_use_end",
		"turn_end", "assistant_message", "tool_call",
		"tool_result",
		"turn_start", "assistant_start", "text_delta",
		"turn_end", "assistant_message",
		"done",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ordering mismatch:\n got=%v\nwant=%v", got, want)
	}
}

// TestSynthesizeChunksText verifies a final assistant message is re-chunked into
// paced deltas that reassemble losslessly.
func TestSynthesizeChunksText(t *testing.T) {
	long := strings.Repeat("x", 20) // ceil(20/6) = 4 chunks
	rows := []core.ReplayRow{
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleUser, provider.TextBlock{Text: "go"})},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleAssistant, provider.TextBlock{Text: long})},
	}
	frames := Synthesize(rows, Options{})
	deltas := 0
	var reassembled strings.Builder
	maxRunes := DefaultPace().TextRunes
	for _, f := range frames {
		if d, ok := f.Event.(core.EvTextDelta); ok {
			deltas++
			reassembled.WriteString(d.Delta)
			if n := len([]rune(d.Delta)); n > maxRunes {
				t.Errorf("chunk of %d runes exceeds pace window %d", n, maxRunes)
			}
		}
	}
	if deltas != 4 {
		t.Errorf("got %d text deltas, want 4", deltas)
	}
	if reassembled.String() != long {
		t.Errorf("reassembled text = %q, want %q", reassembled.String(), long)
	}
}

// TestSynthesizeModes checks that effective mode animates compaction and raw
// mode plays straight through it.
func TestSynthesizeModes(t *testing.T) {
	rows := []core.ReplayRow{
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleUser, provider.TextBlock{Text: "a"})},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleAssistant, provider.TextBlock{Text: "b"})},
		{Kind: core.ReplayRowCompaction, Checkpoint: []provider.Message{msg(provider.RoleUser, provider.TextBlock{Text: "sum"})}},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleUser, provider.TextBlock{Text: "c"})},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleAssistant, provider.TextBlock{Text: "d"})},
	}
	eff := eventTypes(Synthesize(rows, Options{Mode: ModeEffective}))
	if !slices.Contains(eff, "compact_start") || !slices.Contains(eff, "compact_end") {
		t.Errorf("effective mode should animate compaction, got %v", eff)
	}
	raw := eventTypes(Synthesize(rows, Options{Mode: ModeRaw}))
	if slices.Contains(raw, "compact_start") || slices.Contains(raw, "compact_end") {
		t.Errorf("raw mode should not animate compaction, got %v", raw)
	}
}

// TestSynthesizeSyntheticUserStaysInPrompt ensures a synthetic (host-injected)
// user message doesn't split the prompt: no extra EvDone before it.
func TestSynthesizeSyntheticUserStaysInPrompt(t *testing.T) {
	synthetic := msg(provider.RoleUser, provider.TextBlock{Text: "continue"})
	synthetic.Meta = map[string]string{core.MetaSynthetic: "true"}
	rows := []core.ReplayRow{
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleUser, provider.TextBlock{Text: "start"})},
		{Kind: core.ReplayRowMessage, Message: msg(provider.RoleAssistant,
			provider.TextBlock{Text: "ok"},
			provider.ToolCallBlock{ID: "t1", Name: "x", Arguments: []byte(`{}`)},
		)},
		{Kind: core.ReplayRowMessage, Message: synthetic},
	}
	frames := Synthesize(rows, Options{Mode: ModeRaw})
	// The one EvDone is the synthesized close at end-of-rows, not one before
	// the synthetic user message.
	dones := 0
	for _, f := range frames {
		if _, ok := f.Event.(core.EvDone); ok {
			dones++
		}
	}
	if dones != 1 {
		t.Errorf("got %d EvDone, want 1 (trailing close only)", dones)
	}
	if um, ok := frames[len(frames)-2].Event.(core.EvUserMessage); !ok || !um.Synthetic {
		t.Errorf("expected synthetic user message before trailing done, got %T", frames[len(frames)-2].Event)
	}
}
