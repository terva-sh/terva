package workspace

import (
	"context"
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestForkSessionBranchesTranscript drives the Phase-1 sessions.fork verb: a
// branch mints a new parent-linked session that keeps the parent's transcript
// through the cut and inherits its immersive experience, while the parent itself
// is untouched.
func TestForkSessionBranchesTranscript(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	parentInfo, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	parent := w.live(parentInfo.ID)
	base := []provider.Message{
		swipeMsg(provider.RoleUser, "u0"),
		swipeMsg(provider.RoleAssistant, "a0"),
		swipeMsg(provider.RoleUser, "u1"),
		swipeMsg(provider.RoleAssistant, "a1"),
	}
	for _, m := range base {
		if err := parent.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	parent.agent.SetMessages(base)

	// Branch keeping messages 0..1 (through the first assistant reply).
	childInfo, err := w.ForkSession(context.Background(), parentInfo.ID, 1)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if childInfo.ID == parentInfo.ID {
		t.Fatal("fork must mint a new session id")
	}
	if childInfo.Experience != "chat" {
		t.Errorf("child experience = %q, want chat (inherited)", childInfo.Experience)
	}

	child := w.live(childInfo.ID)
	if child == nil {
		t.Fatal("forked session is not live")
	}
	if got := reviseTexts(child.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Errorf("child transcript = %v, want [u0 a0]", got)
	}
	if child.sess.Meta.Parent != parent.sess.Meta.ID {
		t.Errorf("child parent = %q, want %q", child.sess.Meta.Parent, parent.sess.Meta.ID)
	}
	if child.sess.Meta.ForkPoint != 2 {
		t.Errorf("child fork_point = %d, want 2 (messages kept)", child.sess.Meta.ForkPoint)
	}

	// The parent is untouched — branching is non-destructive.
	if got := reviseTexts(parent.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0", "u1", "a1"}) {
		t.Errorf("parent transcript changed: %v", got)
	}

	// A negative index is a clean bad-request, not a panic.
	if _, err := w.ForkSession(context.Background(), parentInfo.ID, -1); err == nil {
		t.Error("fork at a negative index should error")
	}
}
