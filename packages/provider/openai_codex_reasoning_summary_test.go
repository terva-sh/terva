package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// codexSummaryFrames is the reasoning half of a real gpt-5.6-sol response
// captured from chatgpt.com/backend-api/codex/responses with
// reasoning.summary="auto". The shape that matters: the summary arrives as
// SEVERAL parts under one output_index, told apart by summary_index, and the
// completed item then repeats all of them in its `summary` array.
const codexSummaryFrames = `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning"}}

data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}

data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"**Identifying inconsistency**"}

data: {"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0}

data: {"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"**Identifying inconsistency**"}}

data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":""}}

data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"**Concluding answer**"}

data: {"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":1}

data: {"type":"response.reasoning_summary_part.done","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":"**Concluding answer**"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"OPAQUE","summary":[{"type":"summary_text","text":"**Identifying inconsistency**"},{"type":"summary_text","text":"**Concluding answer**"}]}}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":59,"output_tokens":285}}}

`

func codexReasoningFromFrames(t *testing.T, frames string) ReasoningBlock {
	t.Helper()
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(frames))}
	out := make(chan Event, 64)
	go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)

	var done *EventDone
	for ev := range out {
		if d, ok := ev.(EventDone); ok {
			dd := d
			done = &dd
		}
	}
	if done == nil {
		t.Fatal("no EventDone")
	}
	for _, ct := range done.Message.Content {
		if rb, ok := ct.(ReasoningBlock); ok {
			return rb
		}
	}
	t.Fatalf("no ReasoningBlock in %+v", done.Message.Content)
	return ReasoningBlock{}
}

// A multi-part summary is persisted as separate sections, not fused into one
// run-on line and not duplicated. Two parts is the whole point of this
// fixture: a single-part summary passes with or without the fix, so it would
// not have caught either defect. The two defects, both latent until summaries
// were requested:
//
//   - the streaming deltas were concatenated with no separator, fusing
//     "**A**" and "**B**" into "**A****B**";
//   - the completed item then appended the same parts AGAIN, so every summary
//     landed twice.
func TestCodexMultiPartSummaryIsSeparatedAndNotDuplicated(t *testing.T) {
	rb := codexReasoningFromFrames(t, codexSummaryFrames)

	const want = "**Identifying inconsistency**\n\n**Concluding answer**"
	if rb.Summary != want {
		t.Errorf("Summary = %q\nwant %q", rb.Summary, want)
	}
	if n := strings.Count(rb.Summary, "**Identifying inconsistency**"); n != 1 {
		t.Errorf("first part appears %d times, want 1 (duplicate accumulation)", n)
	}
	if strings.Contains(rb.Summary, "****") {
		t.Errorf("adjacent parts fused with no separator: %q", rb.Summary)
	}
	if rb.Encrypted != "OPAQUE" {
		t.Errorf("Encrypted = %q, want OPAQUE — the blob must survive alongside the summary", rb.Encrypted)
	}
	if rb.ID != "rs_1" {
		t.Errorf("ID = %q, want rs_1", rb.ID)
	}
}

// The delta path is the fallback when a completed item carries no summary
// array; it must separate parts on its own rather than relying on the
// completed item to rewrite them.
func TestCodexSummaryDeltaFallbackSeparatesParts(t *testing.T) {
	frames := strings.Replace(codexSummaryFrames,
		`,"summary":[{"type":"summary_text","text":"**Identifying inconsistency**"},{"type":"summary_text","text":"**Concluding answer**"}]`,
		"", 1)
	if strings.Contains(frames, `"summary":[`) {
		t.Fatal("fixture still carries a summary array; the replace did not apply")
	}
	rb := codexReasoningFromFrames(t, frames)

	const want = "**Identifying inconsistency**\n\n**Concluding answer**"
	if rb.Summary != want {
		t.Errorf("Summary = %q\nwant %q", rb.Summary, want)
	}
}

// The summary is requested only when configured, and its absence leaves the
// request byte-identical to one built before this field existed.
func TestCodexRequestsSummaryOnlyWhenConfigured(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)

	on, err := c.buildRequest(Request{Model: "gpt-5.6-sol", Reasoning: "medium", ReasoningSet: true, ReasoningSummary: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if on.Reasoning == nil || on.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning = %+v, want summary=auto", on.Reasoning)
	}

	off, err := c.buildRequest(Request{Model: "gpt-5.6-sol", Reasoning: "medium", ReasoningSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if off.Reasoning == nil || off.Reasoning.Summary != "" {
		t.Fatalf("reasoning = %+v, want no summary", off.Reasoning)
	}
	// omitempty is what makes "disabled ⇒ unchanged on the wire" true rather
	// than merely intended: assert the serialized body carries no summary key.
	body, err := json.Marshal(off)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"summary"`) {
		t.Errorf("disabled request serialized a summary key: %s", body)
	}
}

// A model with reasoning off sends no reasoning block at all, so it cannot
// ask for a summary however the setting is configured.
func TestCodexNoSummaryWithoutReasoning(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model: "gpt-5.6-sol", Reasoning: "", ReasoningSet: true, ReasoningSummary: "detailed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Reasoning != nil {
		t.Fatalf("reasoning = %+v, want nil when reasoning is off", wire.Reasoning)
	}
}

// Replay sends the encrypted blob back verbatim and deliberately drops the
// summary: the blob is what the model consumes, while the summary is a
// human-facing précis that would otherwise be re-billed as input on every
// following turn. The backend accepts either shape (verified live), so this
// pins a cost decision, not a compatibility one.
func TestCodexReplayKeepsBlobAndDropsSummary(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model: "gpt-5.6-sol",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
			{Role: RoleAssistant, Content: []Content{
				ReasoningBlock{ID: "rs_1", Summary: "**A**\n\n**B**", Encrypted: "OPAQUE"},
				TextBlock{Text: "hello"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var item *codexReasoningItem
	for _, in := range wire.Input {
		if ri, ok := in.(codexReasoningItem); ok {
			rr := ri
			item = &rr
		}
	}
	if item == nil {
		t.Fatalf("no reasoning item replayed in %+v", wire.Input)
	}
	if item.EncryptedContent != "OPAQUE" || item.ID != "rs_1" {
		t.Errorf("replayed item = %+v, want the blob and id verbatim", item)
	}
	if len(item.Summary) != 0 {
		t.Errorf("summary replayed (%+v); it should be dropped to avoid re-billing it every turn", item.Summary)
	}
	// The field must still serialize as an empty array, which is the shape
	// terva has always sent and the one proven in production.
	body, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"summary":[]`) {
		t.Errorf("replayed reasoning item = %s, want summary:[]", body)
	}
}

func TestNormalizeReasoningSummary(t *testing.T) {
	cases := map[string]string{
		"auto": "auto", "AUTO": "auto", " auto ": "auto", "on": "auto", "true": "auto",
		"concise": "concise", "short": "concise",
		"detailed": "detailed", "full": "detailed", "long": "detailed",
		// Anything unrecognized fails OFF rather than reaching the API: this
		// field rides every request, so forwarding a typo would 400 every turn
		// instead of quietly omitting the summary.
		"": "", "off": "", "yes-please": "", "auto ,detailed": "",
	}
	for in, want := range cases {
		if got := NormalizeReasoningSummary(in); got != want {
			t.Errorf("NormalizeReasoningSummary(%q) = %q, want %q", in, got, want)
		}
	}
}
