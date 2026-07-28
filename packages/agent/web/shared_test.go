//go:build terva_web

package web

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/testsupport"
)

// sharedServer starts a mux over a throwaway $TERVA_HOME and publishes one file
// into it, returning the URL that should serve it.
func sharedServer(t *testing.T, name, body string) (*httptest.Server, attach.Ref) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "secret"}))
	t.Cleanup(srv.Close)

	src := filepath.Join(testsupport.TempDir(t), name)
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := attach.NewShareStore().Publish("ses_1", src, "")
	if err != nil {
		t.Fatal(err)
	}
	return srv, ref
}

func sharedURL(srv *httptest.Server, sess, id string) string {
	return srv.URL + sharedPath + sess + "/" + id
}

func TestSharedServesThePublishedBytes(t *testing.T) {
	srv, ref := sharedServer(t, "report.pdf", "%PDF-1.4 body")

	resp := authedGet(t, sharedURL(srv, "ses_1", ref.ID), "secret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := make([]byte, 64)
	n, _ := resp.Body.Read(got)
	if string(got[:n]) != "%PDF-1.4 body" {
		t.Errorf("body = %q, want the published bytes", got[:n])
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "pdf") {
		t.Errorf("Content-Type = %q, want a pdf type derived from the file", ct)
	}
}

// The route serves user content from the app's own origin, where the session
// cookie lives. Every one of these headers is load-bearing.
func TestSharedSendsTheContentDefenceHeaders(t *testing.T) {
	srv, ref := sharedServer(t, "notes.txt", "hello")

	resp := authedGet(t, sharedURL(srv, "ses_1", ref.ID), "secret")
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Errorf("Content-Security-Policy = %q, want the sandbox directive over the app's CSP", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment by default", got)
	}
}

// The inline allowlist, and the two entries it must never grow.
//
// An SVG is an image by every classification in this codebase and is also a
// script host; served inline it would execute in the origin holding the session
// cookie. HTML is the same trap wearing its own name. Asking for ?inline=1 must
// not be enough to get either.
func TestSharedRendersInlineOnlyForSafeMediaTypes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		wantInline bool
	}{
		{"chart.png", "\x89PNG\r\n\x1a\nrest", true},
		{"clip.mp3", "ID3 audio", true},
		// These four are the ones the derivation used to get wrong, each in its
		// own way: .mp4 resolved only where the host had a MIME database (CI's
		// container did not), and .wav/.flac resolved to the x- spellings on
		// macOS, which this allowlist does not carry. Every one of them turned a
		// player into a download on some machine. The bodies below sniff as
		// nothing, so only the pinned table can produce the answer.
		{"clip.mp4", "not really an mp4", true},
		{"clip.webm", "not really a webm", true},
		{"voice.wav", "not really a wav", true},
		{"song.flac", "not really a flac", true},
		// .aac derived fine and simply had no entry in the allowlist, so it
		// downloaded instead of playing — the same end as the .wav bug, reached
		// from the other table. inline_coverage_test.go is what now makes that
		// pairing a failure instead of something to notice later.
		{"voice.aac", "not really an aac", true},
		// Declined on purpose rather than missed: no browser plays either
		// natively, so an inline response would be a dead player where a
		// download works. Named in declinedInline with that reason.
		{"clip.mov", "not really a mov", false},
		{"clip.mkv", "not really an mkv", false},
		{"logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>fetch("/ws")</script></svg>`, false},
		{"page.html", "<html><script>fetch('/ws')</script></html>", false},
		{"export.csv", "a,b\n1,2\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, ref := sharedServer(t, tc.name, tc.body)
			resp := authedGet(t, sharedURL(srv, "ses_1", ref.ID)+"?inline=1", "secret")
			defer resp.Body.Close()

			got := resp.Header.Get("Content-Disposition")
			inline := strings.HasPrefix(got, "inline")
			if inline != tc.wantInline {
				t.Errorf("Content-Disposition = %q (inline=%v), want inline=%v", got, inline, tc.wantInline)
			}
		})
	}
}

// Even an allowlisted type downloads unless inline was ASKED for, so a link
// pasted in the address bar saves the file rather than rendering it.
func TestSharedDownloadsUnlessInlineIsRequested(t *testing.T) {
	srv, ref := sharedServer(t, "chart.png", "\x89PNG\r\n\x1a\n")

	resp := authedGet(t, sharedURL(srv, "ses_1", ref.ID), "secret")
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment without ?inline=1", got)
	}
}

// The filename rides a header, so a name carrying a quote or a semicolon must
// not be able to forge a second parameter into it. Two independent things stop
// that — the store strips the characters on the way to disk, and
// mime.FormatMediaType quotes what is left — so the assertion is the property
// itself: parse the header back and there is exactly one filename.
func TestSharedCannotHaveASecondFilenameForgedIntoIt(t *testing.T) {
	srv, _ := sharedServer(t, "plain.txt", "x")
	src := filepath.Join(testsupport.TempDir(t), "src.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := attach.NewShareStore().Publish("ses_1", src, `evil"; filename="owned.exe`)
	if err != nil {
		t.Fatal(err)
	}

	resp := authedGet(t, sharedURL(srv, "ses_1", ref.ID), "secret")
	defer resp.Body.Close()

	raw := resp.Header.Get("Content-Disposition")
	kind, params, err := mime.ParseMediaType(raw)
	if err != nil {
		t.Fatalf("Content-Disposition %q does not parse: %v", raw, err)
	}
	if kind != "attachment" {
		t.Errorf("disposition = %q, want attachment", kind)
	}
	// One parameter, and the injection attempt is INSIDE its value rather than
	// beside it: the quote and the semicolon became part of the name.
	if len(params) != 1 {
		t.Errorf("params = %v, want only a filename", params)
	}
	if got := params["filename"]; !strings.HasPrefix(got, "evil") || strings.ContainsAny(got, `";`) {
		t.Errorf("filename = %q, want the whole hostile name collapsed into one flat token", got)
	}
	// What it is NOT asked to do: police extensions. An agent that wants to hand
	// over an .exe can simply name it one, and a download route is not the place
	// to relitigate that — the user is downloading from their own agent.
}

// <audio> and <video> seek with Range requests; without 206 the player looks
// broken on anything longer than a few seconds.
func TestSharedAnswersARangeRequest(t *testing.T) {
	srv, ref := sharedServer(t, "clip.mp3", "0123456789")

	req, _ := http.NewRequest("GET", sharedURL(srv, "ses_1", ref.ID), nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Range", "bytes=2-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	got := make([]byte, 16)
	n, _ := resp.Body.Read(got)
	if string(got[:n]) != "2345" {
		t.Errorf("range body = %q, want 2345", got[:n])
	}
}

func TestSharedRequiresAuth(t *testing.T) {
	srv, ref := sharedServer(t, "report.pdf", "secret contents")

	resp := authedGet(t, sharedURL(srv, "ses_1", ref.ID), "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d without a token, want 401", resp.StatusCode)
	}
}

// Everything that does not resolve is one answer. In particular the session
// segment is part of the key, so naming another session's id gets nothing.
func TestSharedRefusesWhatDoesNotResolve(t *testing.T) {
	srv, ref := sharedServer(t, "report.pdf", "SENSITIVE-BODY")

	for _, tc := range []struct{ name, path string }{
		{"another session", sharedPath + "ses_2/" + ref.ID},
		{"unknown id", sharedPath + "ses_1/shr_deadbeefdeadbeef"},
		{"no id", sharedPath + "ses_1/"},
		{"no session", sharedPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := authedGet(t, srv.URL+tc.path, "secret")
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Errorf("%s served 200, want a refusal", tc.path)
			}
		})
	}
}

// A traversal in the URL never reaches the handler — ServeMux cleans the path
// and answers 301 — so asserting a status here would only be testing net/http.
// What matters is that no such request yields the bytes, which is what this
// checks, with redirects left unfollowed so the answer is the server's own.
//
// (A followed redirect lands on the SPA catch-all and returns 200 with the app
// shell, which reads exactly like a served file if you look only at the status.)
func TestSharedTraversalInTheURLYieldsNothing(t *testing.T) {
	srv, ref := sharedServer(t, "report.pdf", "SENSITIVE-BODY")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	for _, path := range []string{
		sharedPath + "ses_1/../../etc/passwd",
		sharedPath + "../ses_1/" + ref.ID,
	} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "SENSITIVE-BODY") {
			t.Errorf("%s served the shared file's bytes", path)
		}
	}
}

// ...and the handler refuses one on its own account, without relying on the
// mux having cleaned the path first. The routing layer's normalization is not
// this handler's contract, and a second registration or a different server
// would not carry it.
func TestServeSharedRefusesATraversalItIsHandedDirectly(t *testing.T) {
	srv, ref := sharedServer(t, "report.pdf", "SENSITIVE-BODY")
	_ = srv

	for _, path := range []string{
		sharedPath + "ses_1/../../../etc/passwd",
		sharedPath + "../attachments/ses_1/" + ref.ID,
		sharedPath + "ses_1/..%2f..%2fetc%2fpasswd",
		sharedPath + "./ses_1/" + ref.ID + "/../../../auth.json",
	} {
		req := httptest.NewRequest("GET", "http://x"+path, nil)
		rec := httptest.NewRecorder()
		serveShared(attach.NewShareStore(), rec, req)

		if rec.Code == http.StatusOK {
			t.Errorf("serveShared(%q) = 200, want a refusal", path)
		}
		if strings.Contains(rec.Body.String(), "SENSITIVE-BODY") {
			t.Errorf("serveShared(%q) served the shared file's bytes", path)
		}
	}
}

func TestSharedRefusesAWrite(t *testing.T) {
	srv, ref := sharedServer(t, "report.pdf", "body")

	req, _ := http.NewRequest(http.MethodPost, sharedURL(srv, "ses_1", ref.ID), strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", resp.StatusCode)
	}
}
