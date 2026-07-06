package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/imagegen"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// fakeBackend returns n identical PNG stubs without any network.
type fakeBackend struct {
	id  string
	err error
}

func (f fakeBackend) ID() string { return f.id }
func (f fakeBackend) Generate(_ context.Context, req imagegen.Request) (imagegen.Result, error) {
	if f.err != nil {
		return imagegen.Result{}, f.err
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("data")...)
	var imgs []imagegen.Image
	for i := 0; i < n; i++ {
		imgs = append(imgs, imagegen.Image{Data: png, MimeType: "image/png"})
	}
	return imagegen.Result{Images: imgs, Backend: f.id, Model: "fake-model"}, nil
}

func newGenTool(t *testing.T) (*GenerateImageTool, string) {
	t.Helper()
	tmp := testsupport.TempDir(t)
	reg := imagegen.NewRegistry()
	reg.Add(fakeBackend{id: "fake"})
	return &GenerateImageTool{CWD: tmp, Sandbox: NewSandbox(tmp), Registry: reg}, tmp
}

func imageBlocks(res []provider.Content) []provider.ImageBlock {
	var out []provider.ImageBlock
	for _, c := range res {
		if b, ok := c.(provider.ImageBlock); ok {
			out = append(out, b)
		}
	}
	return out
}

func TestGenerateImageInline(t *testing.T) {
	tool, _ := newGenTool(t)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"a fox"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if imgs := imageBlocks(res.Content); len(imgs) != 1 || imgs[0].MimeType != "image/png" {
		t.Fatalf("want 1 png image block, got %+v", res.Content)
	}
	// The first block is the text caption naming the backend.
	if txt, ok := res.Content[0].(provider.TextBlock); !ok || txt.Text == "" {
		t.Errorf("first block should be a caption, got %+v", res.Content[0])
	}
}

func TestGenerateImageWritesFile(t *testing.T) {
	tool, tmp := newGenTool(t)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"a fox","path":"assets/hero.png"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "assets", "hero.png")); err != nil {
		t.Errorf("expected the image written to assets/hero.png: %v", err)
	}
}

func TestGenerateImageMultipleFiles(t *testing.T) {
	tool, tmp := newGenTool(t)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"a fox","path":"out.png","n":2}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"out-1.png", "out-2.png"} {
		if _, err := os.Stat(filepath.Join(tmp, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

func TestGenerateImageExtensionFromMime(t *testing.T) {
	tool, tmp := newGenTool(t)
	// No extension on the path → derived from the image MIME type.
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x","path":"pic"}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "pic.png")); err != nil {
		t.Errorf("expected pic.png (extension from MIME): %v", err)
	}
}

func TestGenerateImageRequiresPrompt(t *testing.T) {
	tool, _ := newGenTool(t)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"  "}`), nil); err == nil {
		t.Error("blank prompt should error")
	}
}
