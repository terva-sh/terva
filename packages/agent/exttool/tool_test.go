package exttool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/tools/resultcap"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
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

// The wrapper surfaces the extension's load-bearing declaration through the
// Essential() accessor core.ToolEssential reads — the link that keeps an
// essential tool advertised under lazy visibility. Default is not essential.
func TestEssentialAccessor(t *testing.T) {
	inv := &fakeInvoker{}
	if core.ToolEssential(New(inv, Info{Extension: "index", Name: "index_search", Essential: true})) != true {
		t.Error("Info{Essential:true} must produce a tool core.ToolEssential reports as essential")
	}
	if core.ToolEssential(New(inv, Info{Extension: "index", Name: "index_rebuild"})) != false {
		t.Error("a tool without Essential set must not be essential")
	}
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

// useSpillDir points os.TempDir at a fresh directory so a spill is visible to
// the test and removed with it.
func useSpillDir(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	t.Setenv("TMPDIR", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", dir)
		t.Setenv("TEMP", dir)
	}
	return dir
}

// An extension's text over the cap is cut, the full text spills to a private
// file the pointer names, and images pass untouched.
func TestExecuteTextOverTheCapSpills(t *testing.T) {
	dir := useSpillDir(t)
	big := strings.Repeat("extension output line\n", 3*resultcap.MaxBytes/22)
	raw := []byte{0x1, 0x2, 0x3}
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{Content: []extproto.ContentBlock{
		{Type: "text", Text: big},
		{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(raw)},
	}}}
	res := exec(t, inv, nil)

	if len(res.Content) != 3 {
		t.Fatalf("want cut text, image, pointer; got %d blocks", len(res.Content))
	}
	kept := res.Content[0].(provider.TextBlock).Text
	if len(kept) > resultcap.MaxBytes || !strings.HasPrefix(big, kept) {
		t.Fatalf("inline text is %d bytes, want a prefix of at most %d", len(kept), resultcap.MaxBytes)
	}
	if ib, ok := res.Content[1].(provider.ImageBlock); !ok || string(ib.Data) != string(raw) {
		t.Fatalf("image changed: %#v", res.Content[1])
	}
	details := res.Details.(map[string]any)
	path, _ := details["full_output_path"].(string)
	if path == "" || filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "terva-ext-") {
		t.Fatalf("full_output_path = %q, want terva-ext-<hex>.log in %s", path, dir)
	}
	if details["extension"] != "ext" || details["tool"] != "tool" {
		t.Fatalf("the cap dropped the ownership details: %v", details)
	}
	pointer := res.Content[2].(provider.TextBlock).Text
	if !strings.Contains(pointer, "\nfull output: "+path+"\n") || !strings.Contains(pointer, "Read this file instead of another call to the tool") {
		t.Fatalf("pointer does not send the model to the file: %q", pointer)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != big {
		t.Fatalf("spill holds %d bytes (%v), want the full %d", len(got), err, len(big))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("spill file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestExecuteTextUnderTheCapIsUnchanged(t *testing.T) {
	dir := useSpillDir(t)
	text := strings.Repeat("q", resultcap.MaxBytes)
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{Content: []extproto.ContentBlock{{Type: "text", Text: text}}}}
	res := exec(t, inv, nil)
	if len(res.Content) != 1 || res.Content[0].(provider.TextBlock).Text != text {
		t.Fatalf("a result at the limit changed: %d blocks", len(res.Content))
	}
	if _, ok := res.Details.(map[string]any)["full_output_path"]; ok {
		t.Fatal("a result at the limit names a spill file")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a result at the limit wrote %d file(s)", len(entries))
	}
}

func TestExecuteEmptyContentDefensiveFill(t *testing.T) {
	inv := &fakeInvoker{resp: extproto.ToolResultFromExt{}}
	res := exec(t, inv, nil)
	if len(res.Content) != 1 {
		t.Fatalf("empty content should be backfilled with one note, got %d", len(res.Content))
	}
}
