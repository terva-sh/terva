package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The repair has one production caller per provider and it lives inside the
// stream loop, so exercising RepairToolArguments directly proves nothing about
// whether the loop actually calls it. This drives the real Anthropic SSE path
// with the frames a live stream sent, and asserts on what came out the far end.
//
// Anthropic streams input_json_delta fragments verbatim — the text is whatever
// the model typed — so a tab-indented Go edit arrives with real tab bytes
// inside a JSON string, which is a syntax error.
func TestAnthropicStreamRepairsTabIndentedToolArguments(t *testing.T) {
	// Split mid-string exactly like a token stream does, with the raw tab
	// landing inside one fragment rather than on a fragment boundary.
	fragments := []string{
		`{"path":"skills.go",`,
		`"edits":[{"oldText":"func f() error {`,
		"\n\treturn nil\n}\",",
		`"newText":"func f() error {`,
		"\n\treturn errU\n}\"}]}",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if fl != nil {
				fl.Flush()
			}
		}
		write("event: message_start\ndata: " + `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n")
		write("event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_live","name":"edit","input":{}}}` + "\n\n")
		for _, frag := range fragments {
			// partial_json is itself a JSON string field, so the fragment is
			// encoded here the way Anthropic encodes it on the wire. The raw
			// tab only reappears once terva concatenates the decoded pieces —
			// which is precisely why nothing upstream catches it.
			enc, err := json.Marshal(frag)
			if err != nil {
				t.Error(err)
				return
			}
			write("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + string(enc) + `}}` + "\n\n")
		}
		write("event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n")
		write("event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}` + "\n\n")
		write("event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n")
	}))
	defer srv.Close()

	// Sanity: the fragments really do concatenate into something unparseable,
	// so a green result below cannot come from a fixture that was fine anyway.
	if joined := strings.Join(fragments, ""); json.Valid([]byte(joined)) {
		t.Fatalf("fixture concatenates to valid JSON — it no longer reproduces the defect: %q", joined)
	}

	c := NewAnthropic("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "claude-opus-5"})
	if err != nil {
		t.Fatal(err)
	}
	var done EventDone
	for ev := range evs {
		if d, ok := ev.(EventDone); ok {
			done = d
		}
	}
	if done.Err != nil {
		t.Fatalf("stream error: %v", done.Err)
	}

	var call ToolCallBlock
	for _, blk := range done.Message.Content {
		if tc, ok := blk.(ToolCallBlock); ok {
			call = tc
		}
	}
	if call.ID == "" {
		t.Fatalf("no tool call in the assembled message: %+v", done.Message.Content)
	}
	if !json.Valid(call.Arguments) {
		t.Fatalf("arguments still unparseable after the stream: %s", call.Arguments)
	}

	var got struct {
		Path  string `json:"path"`
		Edits []struct {
			OldText string `json:"oldText"`
			NewText string `json:"newText"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(call.Arguments, &got); err != nil {
		t.Fatalf("unmarshal arguments: %v", err)
	}
	if got.Path != "skills.go" || len(got.Edits) != 1 {
		t.Fatalf("arguments did not survive the repair: %+v", got)
	}
	// The tabs must still BE tabs. An edit whose oldText lost its indentation
	// would parse cleanly and then fail to match the file on disk — a repair
	// that turns a JSON error into a confusing no-match is not a fix.
	if want := "func f() error {\n\treturn nil\n}"; got.Edits[0].OldText != want {
		t.Errorf("oldText = %q, want %q", got.Edits[0].OldText, want)
	}

	// And the turn must now be writable to a session, which is what failed
	// before: an invalid RawMessage takes the whole assistant message with it.
	if _, err := json.Marshal(struct {
		Arguments json.RawMessage `json:"arguments"`
	}{call.Arguments}); err != nil {
		t.Errorf("assistant turn still cannot be persisted: %v", err)
	}
}
