package workspace

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// titleStubServer serves an openai-wire streaming completion that replies
// with a fixed title, capturing the request body for seed assertions.
func titleStubServer(t *testing.T, title string, gotBody *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		buf := make([]byte, 64*1024)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		*gotBody = b.String()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", title)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// TestGenerateSessionTitleColdSession drives the sessions.generate_title verb
// end-to-end on a cold (on-disk, not materialized) session against a stub
// openai-wire server: the seed reaches the model, the reply persists as a
// rename row, and — deliberately — no auto_title config is present, because
// the toggle governs the AUTOMATIC pass only, never an explicit request.
func TestGenerateSessionTitleColdSession(t *testing.T) {
	tmp := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tmp)

	var body string
	srv := titleStubServer(t, "Parser Unicode Refactor", &body)
	defer srv.Close()

	sess, err := core.NewSession(tmp, tmp, "openai-compatible", "fake-model", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.AppendMessage(provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "help me refactor the parser, it chokes on unicode"}}})
	// A manual rename beforehand: an explicit generate must overwrite it.
	_ = core.RenameSession(sess.Path, "my manual name")
	sess.Close()
	id := build.SessionIDFromPath(sess.Path)

	w := &Workspace{
		root: tmp, cwd: tmp, version: "test",
		sessions: map[string]*wsSession{},
		args:     build.Args{Provider: "openai-compatible", BaseURL: srv.URL, APIKey: "k", Model: "fake-model", CWD: tmp},
	}
	title, gerr := w.GenerateSessionTitle(context.Background(), id)
	if gerr != nil {
		t.Fatalf("GenerateSessionTitle: %v", gerr)
	}
	if title != "Parser Unicode Refactor" {
		t.Fatalf("title = %q", title)
	}
	if !strings.Contains(body, "help me refactor the parser") {
		t.Fatalf("seed did not reach the model; request body: %s", body)
	}
	got := core.DescribeSessions(tmp, tmp)
	if len(got) == 0 || got[0].Title != "Parser Unicode Refactor" {
		t.Fatalf("generated title not persisted (manual rename should be overwritten): %+v", got)
	}
}

// A session with no user/assistant text (or no such session at all) refuses
// cleanly instead of burning a model call.
func TestGenerateSessionTitleRefusals(t *testing.T) {
	tmp := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tmp)
	w := &Workspace{root: tmp, cwd: tmp, version: "test", sessions: map[string]*wsSession{}}

	if _, err := w.GenerateSessionTitle(context.Background(), "20260101-000000-deadbeef"); err == nil {
		t.Fatal("missing session should error")
	}
	if _, err := w.GenerateSessionTitle(context.Background(), "../../etc/passwd"); err == nil {
		t.Fatal("invalid id should error")
	}

	sess, err := core.NewSession(tmp, tmp, "openai-compatible", "fake-model", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// meta-only file: no messages → nothing to title. Keep a rename row so
	// Close doesn't prune the empty session before the call.
	_ = core.RenameSession(sess.Path, "empty")
	sess.Close()
	if _, err := w.GenerateSessionTitle(context.Background(), build.SessionIDFromPath(sess.Path)); err == nil {
		t.Fatal("empty transcript should error, not call the model")
	}
}

// The live path lands through applyTitle: in-memory title, rename row, and a
// session_updated broadcast — every open client converges without a refresh.
func TestGenerateSessionTitleLiveBroadcasts(t *testing.T) {
	tmp := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tmp)

	var body string
	srv := titleStubServer(t, "Bridge Reconnect Race", &body)
	defer srv.Close()

	sess, err := core.NewSession(tmp, tmp, "openai-compatible", "fake-model", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.AppendMessage(provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "the bridge reconnect races Stop"}}})
	id := build.SessionIDFromPath(sess.Path)

	w := &Workspace{
		root: tmp, cwd: tmp, version: "test",
		sessions: map[string]*wsSession{},
		args:     build.Args{Provider: "openai-compatible", BaseURL: srv.URL, APIKey: "k", Model: "fake-model", CWD: tmp},
	}
	ag := core.NewAgent(nil, "openai-compatible", "fake-model", core.Registry{})
	ag.SetMessages([]provider.Message{{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "the bridge reconnect races Stop"}}}})
	s := &wsSession{id: id, ws: w, sess: sess, hub: newWSHub(), agent: ag, title: "manual name",
		provider: "openai-compatible", model: "fake-model"}
	w.sessions[id] = s
	sub := s.hub.add(nil, false)

	title, gerr := w.GenerateSessionTitle(context.Background(), id)
	if gerr != nil {
		t.Fatalf("GenerateSessionTitle: %v", gerr)
	}
	if title != "Bridge Reconnect Race" {
		t.Fatalf("title = %q", title)
	}
	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventSessionUpdated || ev.Info == nil || ev.Info.Title != "Bridge Reconnect Race" {
		t.Fatalf("want session_updated with new title, got %+v", ev)
	}
}
