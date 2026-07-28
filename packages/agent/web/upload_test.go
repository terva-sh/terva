//go:build terva_web

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/testsupport"
)

// uploadServer starts a mux over a throwaway $TERVA_HOME so a staged file lands
// somewhere the test owns.
func uploadServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	t.Cleanup(srv.Close)
	return srv, home
}

// multipartBody frames one file part the way a browser's FormData does,
// returning the encoded body and the boundary-bearing content type.
func multipartBody(t *testing.T, field, filename, content string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func upload(t *testing.T, srv *httptest.Server, path, token, contentType string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUploadStagesFileAndReturnsAnID(t *testing.T) {
	srv, home := uploadServer(t)
	body, ct := multipartBody(t, "file", "filters.xml", "<filters/>")

	resp := upload(t, srv, "/upload?sess=ses_1", "secret", ct, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var got struct {
		ID, Name, Mime, Kind string
		Size                 int64
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ID, "att_") {
		t.Errorf("id = %q, want an att_ prefix", got.ID)
	}
	if got.Name != "filters.xml" || got.Size != int64(len("<filters/>")) || got.Kind != "document" {
		t.Errorf("response = %+v, want it to describe the staged file", got)
	}

	staged, err := attach.NewStoreAt(filepath.Join(home, attach.DirName)).Resolve("ses_1", got.ID)
	if err != nil {
		t.Fatalf("staged file does not resolve: %v", err)
	}
	if b, err := os.ReadFile(staged.Path); err != nil || string(b) != "<filters/>" {
		t.Errorf("staged bytes = %q, %v; want the uploaded body", b, err)
	}
}

// The response must not hand back a host path: the client names an id, and
// leaking absolute paths would make the browser a place to learn the layout.
func TestUploadResponseCarriesNoHostPath(t *testing.T) {
	srv, home := uploadServer(t)
	body, ct := multipartBody(t, "file", "notes.txt", "hello")

	resp := upload(t, srv, "/upload?sess=ses_1", "secret", ct, body)
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["path"]; ok {
		t.Error("response carries a path field, want the id to be the only handle")
	}
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.Contains(s, home) {
			t.Errorf("field %q = %q leaks the host path", k, s)
		}
	}
}

func TestUploadRequiresAuth(t *testing.T) {
	srv, _ := uploadServer(t)
	body, ct := multipartBody(t, "file", "x.txt", "x")

	resp := upload(t, srv, "/upload?sess=ses_1", "", ct, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

// multipart/form-data is a CORS-simple content type, so the browser sends the
// POST without a preflight and only withholds the RESPONSE — by which time an
// unguarded route would already have written the file. The Origin check is what
// stops a hostile page writing into the staging area.
func TestUploadRejectsCrossOrigin(t *testing.T) {
	srv, home := uploadServer(t)
	body, ct := multipartBody(t, "file", "evil.txt", "evil")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload?sess=ses_1", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	entries, _ := os.ReadDir(filepath.Join(home, attach.DirName))
	if len(entries) != 0 {
		t.Errorf("a cross-origin upload staged %d entries, want none", len(entries))
	}
}

// A same-origin POST is the ordinary case and must still work.
func TestUploadAcceptsSameOrigin(t *testing.T) {
	srv, _ := uploadServer(t)
	body, ct := multipartBody(t, "file", "ok.txt", "ok")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload?sess=ses_1", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Origin", srv.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("same-origin upload: status %d, want 200", resp.StatusCode)
	}
}

func TestUploadRejectsWrongMethod(t *testing.T) {
	srv, _ := uploadServer(t)
	resp := authedGet(t, srv.URL+"/upload?sess=ses_1", "secret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /upload: status %d, want 405", resp.StatusCode)
	}
}

// Staging is per-session, so a default would quietly pool one client's files
// where another's resolve.
func TestUploadRequiresASession(t *testing.T) {
	srv, _ := uploadServer(t)
	body, ct := multipartBody(t, "file", "x.txt", "x")

	for _, path := range []string{"/upload", "/upload?sess=", "/upload?sess=%20"} {
		resp := upload(t, srv, path, "secret", ct, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s: status %d, want 400", path, resp.StatusCode)
		}
	}
}

func TestUploadRejectsBodyWithNoFile(t *testing.T) {
	srv, _ := uploadServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("notafile", "just a field")
	_ = mw.Close()

	resp := upload(t, srv, "/upload?sess=ses_1", "secret", mw.FormDataContentType(), buf.Bytes())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

// The error body is JSON because the caller is fetch(): a text/plain error
// surfaces in the panel as an opaque status code with nothing to show the user.
func TestUploadErrorsAreJSON(t *testing.T) {
	srv, _ := uploadServer(t)
	body, ct := multipartBody(t, "file", "x.txt", "x")

	resp := upload(t, srv, "/upload", "secret", ct, body)
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content-type = %q, want application/json", got)
	}
	var payload struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error == "" {
		t.Error("error body carries no message for the panel to show")
	}
}

// A hostile filename decides how the file is LABELED, never where it lands.
func TestUploadNeutralizesHostileFilename(t *testing.T) {
	srv, home := uploadServer(t)
	body, ct := multipartBody(t, "file", "../../../../etc/passwd", "pwned")

	resp := upload(t, srv, "/upload?sess=ses_1", "secret", ct, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var got struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&got)

	staged, err := attach.NewStoreAt(filepath.Join(home, attach.DirName)).Resolve("ses_1", got.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(home, attach.DirName, "ses_1")
	if dir := filepath.Dir(staged.Path); dir != want {
		t.Errorf("staged in %q, want %q", dir, want)
	}
}

// --- the size bound ---
//
// The route's own arithmetic, driven at a bound small enough to run: a real
// 100 MB body on every CI run would buy nothing these do not already prove.
// What is checked here is the ROUTE — that the MaxBytesReader is placed so an
// at-limit file survives its own multipart framing, and that the overflow it
// produces is recognized through the wrapping the multipart reader adds. The
// STORE's rejection of an over-limit write is attach_test.go's
// (TestStageRejectsOverLimitAndLeavesNothing, TestStageAcceptsExactlyTheLimit);
// it sits behind this reader and cannot be reached from a body this reader
// already refused.

// boundedUploadServer serves the upload route at an explicit ceiling, over a
// throwaway $TERVA_HOME. It bypasses newMux and therefore the auth gate — what
// is under test is the size path, and TestUploadRequiresAuth already pins that
// the mounted route is gated.
func boundedUploadServer(t *testing.T, max int64) (*httptest.Server, string) {
	t.Helper()
	home := testsupport.TempDir(t)
	root := filepath.Join(home, attach.DirName)
	srv := httptest.NewServer(uploadHandler(attach.NewStoreAt(root), max))
	t.Cleanup(srv.Close)
	return srv, root
}

// The whole point of uploadEnvelopeSlack: a file of EXACTLY the advertised
// limit must land. Without the slack the multipart boundaries and part headers
// wrapped around it push the body past the reader's ceiling, and the daemon
// 413s a file it told the client it would take — the one refusal a user cannot
// act on, because shrinking the file by a byte would not obviously help.
func TestUploadAcceptsAFileOfExactlyTheLimit(t *testing.T) {
	const max = 64 << 10
	srv, root := boundedUploadServer(t, max)
	body, ct := multipartBody(t, "file", "at-the-limit.bin", strings.Repeat("x", max))

	resp := upload(t, srv, "/upload?sess=ses_1", "", ct, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 — the envelope slack does not cover a file at the limit", resp.StatusCode)
	}
	var got struct{ Size int64 }
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Size != max {
		t.Errorf("staged size = %d, want %d", got.Size, int64(max))
	}
	if _, err := os.Stat(filepath.Join(root, "ses_1")); err != nil {
		t.Errorf("nothing landed on disk: %v", err)
	}
}

// Past the slack, the reader trips. net/http hands back a *MaxBytesError, but
// the multipart reader wraps it on the way out — which is why isBodyTooLarge
// has a string fallback under its errors.As, and why this asserts the STATUS
// rather than the error value: the fallback existing is not evidence it fires.
func TestUploadRefusesAnOverLimitFileWith413(t *testing.T) {
	const max = 64 << 10
	srv, root := boundedUploadServer(t, max)
	body, ct := multipartBody(t, "file", "too-big.bin", strings.Repeat("x", max+uploadEnvelopeSlack+1))

	resp := upload(t, srv, "/upload?sess=ses_1", "", ct, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413 — an over-limit body was not recognized as one", resp.StatusCode)
	}
	// A refusal the panel can show, not an opaque status: the composer surfaces
	// this string verbatim.
	var payload struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("413 body is not JSON: %v", err)
	}
	if !strings.Contains(payload.Error, "too large") {
		t.Errorf("413 message = %q, want it to say the file was too large", payload.Error)
	}
	// And nothing half-written survives the refusal.
	if entries, err := os.ReadDir(filepath.Join(root, "ses_1")); err == nil && len(entries) > 0 {
		t.Errorf("a refused upload left %d file(s) staged", len(entries))
	}
}

// The message quotes the bound the ROUTE enforces. It used to read
// attach.MaxBytes directly, so a carrier mounted at any other ceiling would
// have told the user a limit it was not applying.
func TestUploadTooLargeMessageQuotesTheRoutesOwnLimit(t *testing.T) {
	srv, _ := boundedUploadServer(t, 8<<20)
	body, ct := multipartBody(t, "file", "too-big.bin", strings.Repeat("x", (8<<20)+uploadEnvelopeSlack+1))

	resp := upload(t, srv, "/upload?sess=ses_1", "", ct, body)
	defer resp.Body.Close()
	var payload struct{ Error string }
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if !strings.Contains(payload.Error, "8 MB") {
		t.Errorf("413 message = %q, want it to quote this route's 8 MB limit", payload.Error)
	}
}
