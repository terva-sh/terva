package agent

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func TestSessionHistoryReader(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	// Write a real session to disk.
	s, err := core.NewSession(home, cwd, "anthropic", "claude-opus-4-5", "test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	_ = s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "find the bug"}}})
	_ = s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "fixed it in foo.go"}}})
	_ = s.Close()

	r := newSessionHistoryReader(home, cwd)

	// List: the session shows up for the active project.
	list := r.ListSessions("ext", "")
	if len(list) != 1 {
		t.Fatalf("ListSessions returned %d, want 1", len(list))
	}
	id := list[0].SessionID
	if id == "" {
		t.Fatal("session has no id")
	}
	if list[0].Messages != 2 {
		t.Errorf("message count = %d, want 2", list[0].Messages)
	}

	// A mismatched project id returns nothing.
	if got := r.ListSessions("ext", "not-this-project"); got != nil {
		t.Errorf("mismatched project should return nil, got %v", got)
	}

	// Read: the transcript comes back flattened to role+text.
	msgs, ok := r.ReadSession("ext", id)
	if !ok {
		t.Fatal("ReadSession not found")
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Text != "find the bug" {
		t.Errorf("unexpected transcript: %+v", msgs)
	}

	// Path-traversal ids are refused.
	if _, ok := r.ReadSession("ext", "../secret"); ok {
		t.Error("traversal id should be refused")
	}
	if _, ok := r.ReadSession("ext", "nope"); ok {
		t.Error("unknown id should be not-found")
	}
}
