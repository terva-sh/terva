package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadText(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "a.txt"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatalf("got %q", got)
	}
}

func TestReadImageMimeFromContentNotExtension(t *testing.T) {
	// A file named .png whose bytes are actually JPEG. The MIME must be
	// sniffed from the content (image/jpeg), not the extension, or
	// providers that validate the declared media type reject the request.
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "shot.png")
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	if err := os.WriteFile(p, jpegBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, SupportsVision: true}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "shot.png"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := res.Content[0].(provider.ImageBlock)
	if !ok {
		t.Fatalf("expected ImageBlock, got %T", res.Content[0])
	}
	if img.MimeType != "image/jpeg" {
		t.Fatalf("mime from extension not corrected: got %s want image/jpeg", img.MimeType)
	}
}

func TestReadOffsetLimit(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("1\n2\n3\n4\n5\n"), 0o644)
	tool := &ReadTool{CWD: dir}
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "a.txt", "offset": 2, "limit": 2}), nil)
	got := res.Content[0].(provider.TextBlock).Text
	// Current output format is raw bytes (no embedded line numbers):
	// the tui draws its own gutter from the `start_line` detail.
	if got != "2\n3\n" {
		t.Fatalf("want \"2\\n3\\n\", got %q", got)
	}
	if start, ok := res.Details.(map[string]any)["start_line"]; !ok || start != 2 {
		t.Errorf("start_line detail want 2, got %v", start)
	}
}

func TestReadBinaryRejected(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "b.bin")
	os.WriteFile(p, []byte{0x00, 0x01, 0x02}, 0o644)
	tool := &ReadTool{CWD: dir}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "b.bin"}), nil); err == nil {
		t.Fatal("want binary rejection")
	}
}

// TestReadImageVisionCapable: a vision-capable ReadTool returns the
// image inline as an ImageBlock the model consumes (today's behavior).
func TestReadImageVisionCapable(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\nfake-pixels"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, SupportsVision: true}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "pic.png"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := res.Content[0].(provider.ImageBlock)
	if !ok {
		t.Fatalf("want ImageBlock, got %T", res.Content[0])
	}
	if img.MimeType != "image/png" {
		t.Fatalf("mime = %q, want image/png", img.MimeType)
	}
}

// TestReadImageNonVision: a non-vision ReadTool returns a successful
// TEXT result (no ImageBlock) that names how to enable vision, instead
// of an image block the provider would silently drop.
func TestReadImageNonVision(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\nfake-pixels"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, SupportsVision: false}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "pic.png"}), nil)
	if err != nil {
		t.Fatalf("non-vision read should not error: %v", err)
	}
	for _, c := range res.Content {
		if _, isImage := c.(provider.ImageBlock); isImage {
			t.Fatal("non-vision read must not return an ImageBlock")
		}
	}
	txt, ok := res.Content[0].(provider.TextBlock)
	if !ok {
		t.Fatalf("want TextBlock, got %T", res.Content[0])
	}
	if !strings.Contains(txt.Text, "image-input") {
		t.Errorf("text should name the models.json capabilities hint, got %q", txt.Text)
	}
	if !strings.Contains(txt.Text, "vision-capable") {
		t.Errorf("text should mention switching to a vision-capable model, got %q", txt.Text)
	}
}

func TestWriteCreatesDirs(t *testing.T) {
	dir := testsupport.TempDir(t)
	tool := &WriteTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "sub/a.txt", "content": "hi"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "sub", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hi" {
		t.Fatalf("got %q", string(b))
	}
}

func TestEditSingle(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("hello world\n"), 0o644)
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":  "a.txt",
		"edits": []map[string]any{{"oldText": "world", "newText": "gopher"}},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hello gopher\n" {
		t.Fatalf("got %q", string(b))
	}
}

func TestEditMultiple(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("a\nb\nc\n"), 0o644)
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "a.txt",
		"edits": []map[string]any{
			{"oldText": "a", "newText": "A"},
			{"oldText": "c", "newText": "C"},
		},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "A\nb\nC\n" {
		t.Fatalf("got %q", string(b))
	}
}

func TestEditAmbiguous(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("x\nx\n"), 0o644)
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":  "a.txt",
		"edits": []map[string]any{{"oldText": "x", "newText": "y"}},
	}), nil)
	if err == nil {
		t.Fatal("want ambiguous error")
	}
}

func TestEditPreservesCRLF(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("hello\r\nworld\r\n"), 0o644)
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":  "a.txt",
		"edits": []map[string]any{{"oldText": "world", "newText": "gopher"}},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hello\r\ngopher\r\n" {
		t.Fatalf("got %q", string(b))
	}
}

func TestBashSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"command": "echo hi"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, "hi") || !strings.Contains(got, "[exit 0]") {
		t.Fatalf("got %q", got)
	}
	if res.IsError {
		t.Fatal("unexpected error flag")
	}
}

func TestBashFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	res, _ := tool.Execute(context.Background(), mustJSON(t, map[string]any{"command": "false"}), nil)
	if !res.IsError {
		t.Fatal("want error")
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, "[exit 1]") {
		t.Fatalf("got %q", got)
	}
}
