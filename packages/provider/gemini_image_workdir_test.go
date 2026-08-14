package provider

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// geminiImageFrame is an SSE frame carrying one inline image, the shape Gemini
// returns for a generated picture.
func geminiImageFrame(t *testing.T) string {
	t.Helper()
	// A tiny but real payload; the client only base64-decodes and writes it.
	data := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0JFIF-not-a-real-jpeg"))
	return `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"inlineData":{"mimeType":"image/jpeg","data":"` + data + `"}}` +
		`]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1450,` +
		`"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]}}`
}

// streamGeminiImage runs one image response with the given Request.WorkingDir
// and returns the path the client reported for the saved image.
func streamGeminiImage(t *testing.T, workingDir string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + geminiImageFrame(t) + "\n\n"))
	}))
	defer srv.Close()

	evs, err := NewGemini("k", srv.URL).Stream(context.Background(), Request{
		Model:      "gemini-3.1-flash-image",
		WorkingDir: workingDir,
		Messages:   []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "draw"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The client reports the saved path as a trailing text block beside the
	// image itself: "Saved image: `<path>`".
	var path string
	for ev := range evs {
		e, ok := ev.(EventDone)
		if !ok {
			continue
		}
		for _, c := range e.Message.Content {
			tb, ok := c.(TextBlock)
			if !ok || !strings.HasPrefix(tb.Text, "Saved image: ") {
				continue
			}
			path = strings.Trim(strings.TrimPrefix(tb.Text, "Saved image: "), "`")
		}
	}
	return path
}

// 🪤 The save joined against "." — the PROCESS working directory. terva never
// chdirs (--cwd moves the agent's workspace, not the process), so a session
// launched from one directory against a workspace in another wrote its
// generated images into the launch directory. Proven live 2026-08-14: process
// cwd /tmp/terva-nb/launchdir, --cwd /tmp/terva-nb/ws, and the JPEG landed in
// launchdir. The model then reported a bare filename the read tool could not
// open, because the read tool resolves against the workspace.
func TestAGeneratedImageLandsInTheWorkingDir(t *testing.T) {
	ws := t.TempDir()

	// Run from a DIFFERENT process cwd, which is the whole point: if the two
	// were the same the defect would be invisible.
	launch := t.TempDir()
	restore := chdir(t, launch)
	defer restore()

	path := streamGeminiImage(t, ws)
	if path == "" {
		t.Fatal("no image path reported")
	}

	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(launch, path)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("reported image path %q does not exist: %v", path, err)
	}

	// The decisive check: the file is in the workspace, not the launch dir.
	inWorkspace, err := filepath.Glob(filepath.Join(ws, "terva-gemini-image-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inWorkspace) != 1 {
		strays, _ := filepath.Glob(filepath.Join(launch, "terva-gemini-image-*"))
		t.Fatalf("found %d images in the workspace %s (want 1); %d landed in the launch dir instead — "+
			"the save is joining against the process cwd", len(inWorkspace), ws, len(strays))
	}
	if strays, _ := filepath.Glob(filepath.Join(launch, "terva-gemini-image-*")); len(strays) != 0 {
		t.Errorf("%d image(s) also written to the launch directory %s", len(strays), launch)
	}
}

// An empty WorkingDir must keep the old behavior exactly: every embedder and
// helper request that never sets the field still works, writing to the process
// cwd as it always did.
func TestAnEmptyWorkingDirStillWritesToTheProcessCwd(t *testing.T) {
	launch := t.TempDir()
	restore := chdir(t, launch)
	defer restore()

	path := streamGeminiImage(t, "")
	if path == "" {
		t.Fatal("no image path reported")
	}
	if filepath.IsAbs(path) {
		t.Errorf("path %q is absolute; an unset WorkingDir should behave as before", path)
	}
	got, err := filepath.Glob(filepath.Join(launch, "terva-gemini-image-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("found %d images in the process cwd, want 1", len(got))
	}
}

// The path handed back must be usable, and the extension must follow the mime
// type rather than defaulting to .png for a JPEG.
func TestTheReportedImagePathMatchesTheMimeType(t *testing.T) {
	ws := t.TempDir()
	path := streamGeminiImage(t, ws)
	if !strings.HasSuffix(path, ".jpg") {
		t.Errorf("path %q does not end in .jpg for an image/jpeg payload", path)
	}
	if filepath.Dir(path) != filepath.Clean(ws) {
		t.Errorf("path %q is not inside the working dir %q", path, ws)
	}
}

// chdir moves the process into dir and returns a restore func. Go's t.Chdir
// arrived in 1.24 and this module targets 1.22.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}
