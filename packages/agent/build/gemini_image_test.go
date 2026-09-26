package build

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// streamTervaGeminiImage runs one image response through terva's own "google"
// registry row, so the saver comes from hostOptions as it does in a session,
// with workingDir as the client's CWD, and returns the path the model is told.
func streamTervaGeminiImage(t *testing.T, workingDir string) string {
	t.Helper()
	data := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0JFIF-not-a-real-jpeg"))
	frame := `{"candidates":[{"content":{"role":"model","parts":[` +
		`{"inlineData":{"mimeType":"image/jpeg","data":"` + data + `"}}` +
		`]},"finishReason":"STOP"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + frame + "\n\n"))
	}))
	defer srv.Close()

	spec, ok := specFor("google")
	if !ok {
		t.Fatal("no google provider")
	}
	evs, err := spec.newClient(clientConfig{Provider: "google", Credential: "k", BaseURL: srv.URL, CWD: workingDir}).Stream(context.Background(), provider.Request{
		Model:    "gemini-3.1-flash-image",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "draw"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for ev := range evs {
		e, ok := ev.(provider.EventDone)
		if !ok {
			continue
		}
		for _, c := range e.Message.Content {
			if tb, ok := c.(provider.TextBlock); ok && strings.HasPrefix(tb.Text, "Saved image: ") {
				path = strings.Trim(strings.TrimPrefix(tb.Text, "Saved image: "), "`")
			}
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
//
// Moved here from the wire with the save itself (TKT-01M35WK08).
func TestAGeneratedImageLandsInTheWorkingDir(t *testing.T) {
	ws, launch := testsupport.TempDir(t), testsupport.TempDir(t)
	// Run from a DIFFERENT process cwd, which is the whole point: if the two
	// were the same the defect would be invisible.
	t.Chdir(launch)

	path := streamTervaGeminiImage(t, ws)
	if path == "" {
		t.Fatal("no image path reported")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("reported image path %q does not exist: %v", path, err)
	}
	if got, _ := filepath.Glob(filepath.Join(ws, "terva-gemini-image-*")); len(got) != 1 {
		t.Fatalf("found %d images in the workspace, want 1", len(got))
	}
	if strays, _ := filepath.Glob(filepath.Join(launch, "terva-gemini-image-*")); len(strays) != 0 {
		t.Errorf("%d image(s) written to the launch directory %s", len(strays), launch)
	}
}

// A client with no CWD (built without a Resolve) writes to the process cwd, as
// the save always did before it knew the workspace.
func TestAClientWithNoCWDWritesToTheProcessCwd(t *testing.T) {
	launch := testsupport.TempDir(t)
	t.Chdir(launch)

	path := streamTervaGeminiImage(t, "")
	if path == "" {
		t.Fatal("no image path reported")
	}
	if filepath.IsAbs(path) {
		t.Errorf("path %q is absolute; a client with no CWD should behave as before", path)
	}
	if got, _ := filepath.Glob(filepath.Join(launch, "terva-gemini-image-*")); len(got) != 1 {
		t.Fatalf("found %d images in the process cwd, want 1", len(got))
	}
}

// The path handed back must be usable, and the extension must follow the mime
// type rather than defaulting to .png for a JPEG.
func TestTheReportedImagePathMatchesTheMimeType(t *testing.T) {
	ws := testsupport.TempDir(t)
	path := streamTervaGeminiImage(t, ws)
	if !strings.HasSuffix(path, ".jpg") {
		t.Errorf("path %q does not end in .jpg for an image/jpeg payload", path)
	}
	if filepath.Dir(path) != filepath.Clean(ws) {
		t.Errorf("path %q is not inside the working dir %q", path, ws)
	}
}

// A session's client saves into the session's workspace because Resolve hands
// its CWD to the client it builds. The engine used to carry the directory to the
// wire on every request (Agent.CWD, Request.WorkingDir); that path is gone
// (TKT-01M35WK1J), so this is the one link left to hold.
func TestResolvedHandsItsCWDToTheClient(t *testing.T) {
	ws := testsupport.TempDir(t)
	if got := (Resolved{CWD: ws}).clientConfig().CWD; got != ws {
		t.Fatalf("clientConfig().CWD = %q, want the Resolve's %q", got, ws)
	}
}
