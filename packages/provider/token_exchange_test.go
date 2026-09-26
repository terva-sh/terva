package provider

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingTransport stands in for a host's HTTP transport: it answers every
// request itself and records where each one went, with its Authorization.
type recordingTransport struct {
	mu   sync.Mutex
	seen []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req.URL.Host+req.URL.Path+" "+req.Header.Get("Authorization"))
	r.mu.Unlock()
	body := `{}`
	switch {
	case strings.HasSuffix(req.URL.Path, "/copilot_internal/v2/token"):
		body = fmt.Sprintf(`{"token":"tid=short;proxy-ep=proxy.copilot.example","expires_at":%d}`, time.Now().Add(time.Hour).Unix())
	case req.URL.Host == "oauth.example":
		body = `{"access_token":"ya29.short","expires_in":3600}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func (r *recordingTransport) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func send(t *testing.T, c *http.Client, url string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func copilotHTTP(t *testing.T, c Client) *http.Client {
	t.Helper()
	v := innerOpenAI(c)
	if v == nil {
		t.Fatal("the Copilot client has no OpenAI client inside it")
	}
	return v.http
}

// A host's HTTP client carries the Copilot token exchange as well as the
// inference request, and the Copilot token wrapper survives: the inference
// request carries the short-lived token, never the PAT.
func TestWithHTTPClientCarriesTheCopilotTokenExchange(t *testing.T) {
	rec := &recordingTransport{}
	c := WithHTTPClient(newGithubCopilotClient("ghp_pat"), &http.Client{Transport: rec})
	send(t, copilotHTTP(t, c), "https://api.individual.githubcopilot.com/chat/completions")

	want := []string{
		"api.github.com/copilot_internal/v2/token Bearer ghp_pat",
		"api.copilot.example/chat/completions Bearer tid=short;proxy-ep=proxy.copilot.example",
	}
	if got := rec.requests(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the host transport saw:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The same for Vertex with an authorized-user credential: the exchange goes to
// the token URI through the host's client, and the rewritten Vertex request
// carries the access token.
func TestWithHTTPClientCarriesTheVertexTokenExchange(t *testing.T) {
	creds := `{"type":"authorized_user","client_id":"id.example","client_secret":"s","refresh_token":"r","token_uri":"https://oauth.example/token"}`
	rec := &recordingTransport{}
	c := WithHTTPClient(newVertex(VertexConfig{Project: "proj", CredentialsJSON: []byte(creds)}), &http.Client{Transport: rec})
	g := innerGemini(c)
	if g == nil {
		t.Fatal("the Vertex client has no Gemini client inside it")
	}
	send(t, g.http, "https://us-central1-aiplatform.googleapis.com/v1beta/models/gemini-x:streamGenerateContent")

	want := []string{
		"oauth.example/token ",
		"us-central1-aiplatform.googleapis.com/v1/projects/proj/locations/us-central1/publishers/google/models/gemini-x:streamGenerateContent Bearer ya29.short",
	}
	if got := rec.requests(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the host transport saw:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Two clients in one process keep separate caches, even for the same PAT:
// each exchanges once through its own transport, and a second request on the
// first client reuses its token.
func TestCopilotClientsKeepSeparateTokenCaches(t *testing.T) {
	first, second := &recordingTransport{}, &recordingTransport{}
	a := WithHTTPClient(newGithubCopilotClient("ghp_same"), &http.Client{Transport: first})
	b := WithHTTPClient(newGithubCopilotClient("ghp_same"), &http.Client{Transport: second})
	send(t, copilotHTTP(t, a), "https://api.individual.githubcopilot.com/chat/completions")
	send(t, copilotHTTP(t, b), "https://api.individual.githubcopilot.com/chat/completions")
	send(t, copilotHTTP(t, a), "https://api.individual.githubcopilot.com/chat/completions")

	exchanges := func(r *recordingTransport) int {
		n := 0
		for _, s := range r.requests() {
			if strings.Contains(s, "copilot_internal/v2/token") {
				n++
			}
		}
		return n
	}
	if got := exchanges(first); got != 1 {
		t.Errorf("the first client exchanged %d times, want 1 (its cache should serve the second request)", got)
	}
	if got := exchanges(second); got != 1 {
		t.Errorf("the second client exchanged %d times, want 1 (a shared cache would have served it the first client's token)", got)
	}
}

// No package-level variable in the wire holds a token cache. A cache there is
// shared by every client in the process, and its exchange ignores the host's
// HTTP client. The check reads every non-test file for a package-level var
// that names a *TokenCache anywhere: as its type, in a composite literal, or
// in a constructor such as newCopilotTokenCache.
func TestNoPackageLevelTokenCache(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			ast.Inspect(gd, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && strings.Contains(id.Name, "TokenCache") {
					t.Errorf("%s: a package-level var names %s; give each client its own cache", fset.Position(id.Pos()), id.Name)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no Go files read; the check would pass on nothing")
	}
}
