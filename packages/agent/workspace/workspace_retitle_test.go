package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// retitleTestSession builds a live wsSession whose transcript already holds a
// compaction anchor plus a recent exchange — the post-compaction state the
// hook fires on — backed by the given stub provider server.
func retitleTestSession(t *testing.T, srvURL, title string, generated bool) *wsSession {
	t.Helper()
	tmp := os.Getenv("TERVA_HOME")
	sess, err := core.NewSession(tmp, tmp, "openai-compatible", "fake-model", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	w := &Workspace{
		root: tmp, cwd: tmp, version: "test",
		sessions: map[string]*wsSession{},
		args:     build.Args{Provider: "openai-compatible", BaseURL: srvURL, APIKey: "k", Model: "fake-model", CWD: tmp},
	}
	ag := core.NewAgent(nil, "openai-compatible", "fake-model", core.Registry{})
	summary := provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "## Context Summary (compacted)\n\nbuilt the lexer, parser next"}},
		Meta:    map[string]string{"compaction": "true"}}
	recent := provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "now the parser error recovery"}}}
	ag.SetMessages([]provider.Message{summary, recent})
	s := &wsSession{id: build.SessionIDFromPath(sess.Path), ws: w, sess: sess, hub: newWSHub(), agent: ag,
		provider: "openai-compatible", model: "fake-model", title: title, titleGenerated: generated}
	w.sessions[s.id] = s
	return s
}

func enableAutoTitle(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"auto_title": true}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// The happy path: a machine-generated title, auto_title on → the hook
// replaces it from the fresh compaction anchor and broadcasts.
func TestRetitleAfterCompactionReplacesGeneratedTitle(t *testing.T) {
	tmp := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tmp)
	enableAutoTitle(t, tmp)
	var body string
	srv := titleStubServer(t, "Parser Error Recovery", &body)
	defer srv.Close()

	s := retitleTestSession(t, srv.URL, "old generated title", true)
	sub := s.hub.add(nil, false)
	s.retitleAfterCompaction(context.Background())

	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventSessionUpdated || ev.Info == nil || ev.Info.Title != "Parser Error Recovery" {
		t.Fatalf("want session_updated with the new title, got %+v", ev)
	}
	if body == "" {
		t.Fatal("model was never called")
	}
	// Provenance stays machine: a later compaction may refresh again.
	s.mu.Lock()
	gen := s.titleGenerated
	s.mu.Unlock()
	if !gen {
		t.Fatal("refreshed title lost its generated provenance")
	}
}

// A manual rename is never touched — even with auto_title on, the hook must
// return before any model call.
func TestRetitleAfterCompactionSkipsManualTitle(t *testing.T) {
	tmp := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tmp)
	enableAutoTitle(t, tmp)
	var body string
	srv := titleStubServer(t, "should never appear", &body)
	defer srv.Close()

	s := retitleTestSession(t, srv.URL, "my hand-picked name", false)
	s.retitleAfterCompaction(context.Background())

	if body != "" {
		t.Fatal("manual title triggered a model call")
	}
	s.mu.Lock()
	title := s.title
	s.mu.Unlock()
	if title != "my hand-picked name" {
		t.Fatalf("manual title clobbered: %q", title)
	}
}

// auto_title off (the default): the hook spends nothing, exactly like the
// automatic settle pass — the toggle governs unasked tokens.
func TestRetitleAfterCompactionRespectsAutoTitleOff(t *testing.T) {
	tmp := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tmp)
	var body string
	srv := titleStubServer(t, "should never appear", &body)
	defer srv.Close()

	s := retitleTestSession(t, srv.URL, "old generated title", true)
	s.retitleAfterCompaction(context.Background())

	if body != "" {
		t.Fatal("auto_title off still called the model")
	}
}
