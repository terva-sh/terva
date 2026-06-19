package exttool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// fakeInvoker returns a canned result (or error) so the conversion logic
// can be tested without spawning a subprocess.
type fakeInvoker struct {
	resp extproto.ToolResultFromExt
	err  error
	// gotArgs captures what Execute forwarded, so the empty-args
	// normalisation can be asserted.
	gotArgs json.RawMessage
}

func (f *fakeInvoker) InvokeTool(_ context.Context, _ string, args json.RawMessage, _ time.Duration) (extproto.ToolResultFromExt, error) {
	f.gotArgs = args
	return f.resp, f.err
}

func exec(t *testing.T, inv Invoker, args json.RawMessage) core.ToolResult {
	t.Helper()
	tool := New(inv, Info{Extension: "ext", Name: "tool"})
	res, err := tool.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("Execute returned a transport error (should be surfaced as a tool result): %v", err)
	}
	return res
}

func TestExecuteTextRoundTrip(t *testing.T) {
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{
		Content: []extproto.ContentBlock{{Type: "text", Text: "hello"}},
	}}
	res := exec(t, inv, json.RawMessage(`{"k":1}`))
	if res.IsError {
		t.Fatalf("expected success, got is_error")
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 block, got %d", len(res.Content))
	}
	if tb, ok := res.Content[0].(provider.TextBlock); !ok || tb.Text != "hello" {
		t.Fatalf("want text 'hello', got %#v", res.Content[0])
	}
}

func TestExecuteEmptyArgsNormalised(t *testing.T) {
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{
		Content: []extproto.ContentBlock{{Type: "text", Text: "ok"}},
	}}
	_ = exec(t, inv, nil)
	if string(inv.gotArgs) != `{}` {
		t.Fatalf("empty args should normalise to {}, forwarded %q", inv.gotArgs)
	}
}

func TestExecuteImageDecode(t *testing.T) {
	raw := []byte{0x1, 0x2, 0x3}
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{
		Content: []extproto.ContentBlock{{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(raw)}},
	}}
	res := exec(t, inv, nil)
	ib, ok := res.Content[0].(provider.ImageBlock)
	if !ok {
		t.Fatalf("want ImageBlock, got %#v", res.Content[0])
	}
	if ib.MimeType != "image/png" || string(ib.Data) != string(raw) {
		t.Fatalf("image not decoded: %#v", ib)
	}
}

func TestExecuteUndecodableImageErrors(t *testing.T) {
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{
		Content: []extproto.ContentBlock{{Type: "image", Data: "!!!not base64!!!"}},
	}}
	res := exec(t, inv, nil)
	if !res.IsError {
		t.Fatal("undecodable image should mark the result as an error")
	}
}

func TestExecuteInvokeErrorBecomesToolError(t *testing.T) {
	inv := &fakeInvoker{err: errors.New("boom")}
	res := exec(t, inv, nil)
	if !res.IsError {
		t.Fatal("an invoke error must surface as is_error, not a Go error")
	}
}

func TestExecuteEmptyContentDefensiveFill(t *testing.T) {
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{}}
	res := exec(t, inv, nil)
	if len(res.Content) != 1 {
		t.Fatalf("empty content should be backfilled with one note, got %d", len(res.Content))
	}
}
