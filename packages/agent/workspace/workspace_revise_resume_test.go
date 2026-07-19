package workspace

import (
	"context"
	"fmt"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seedResumedTrimmed writes a session with n message rows to disk under a fresh
// TERVA_HOME, then materializes it in a SECOND workspace — the daemon-restart
// path, which trims the in-memory transcript to the last 100 while the file keeps
// all n. It returns the resumed session (in-memory trimmed) and its on-disk path.
// Messages are text "m0".."m<n-1>", alternating user/assistant.
func seedResumedTrimmed(t *testing.T, n int) (*wsSession, string) {
	t.Helper()
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}

	w1, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatal(err)
	}
	info, err := w1.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s1 := w1.live(info.ID)
	if s1 == nil {
		t.Fatal("created session is not live")
	}
	for i := 0; i < n; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		m := provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("m%d", i)}}}
		if err := s1.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	w1.Close()

	// A fresh workspace over the same home/cwd resolves the session cold, exercising
	// the resume + TrimMessagesForResume path (the only place reviseBase is set).
	w2, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w2.Close() })
	s2, err := w2.resolve(info.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	return s2, info.Path
}

// TestReviseReanchorsResumeTrimmedDelete is the regression for the silent
// transcript corruption in the release review: a resumed session is trimmed to the
// last 100 messages in memory, but a delete persisted its amend using the
// in-memory index, so a reload deleted a message ~30 positions earlier than the one
// the user removed. With the re-anchoring, the delete lands on the on-disk message
// the user actually saw.
func TestReviseReanchorsResumeTrimmedDelete(t *testing.T) {
	const n = 130
	s, path := seedResumedTrimmed(t, n)

	// The resume trimmed the in-memory transcript; the file still holds all n.
	if got := len(s.agent.Messages()); got != 100 {
		t.Fatalf("resumed in-memory length = %d, want 100 (trimmed)", got)
	}
	if s.reviseBase != n-100 {
		t.Fatalf("reviseBase = %d, want %d", s.reviseBase, n-100)
	}

	// Delete the LAST visible message (in-memory index 99 == on-disk index 129).
	last := len(s.agent.Messages()) - 1
	if got := reviseText(s.agent.Messages()[last]); got != "m129" {
		t.Fatalf("last visible message = %q, want m129", got)
	}
	if err := s.deleteMessage(s.agent.TranscriptEpoch(), last); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// A reload from disk must show m129 gone and NOTHING else disturbed — the whole
	// point of the fix. Before it, on-disk index 99 (m99) was deleted instead.
	_, reloaded, err := core.OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reloaded) != n-1 {
		t.Fatalf("reloaded length = %d, want %d", len(reloaded), n-1)
	}
	for i := 0; i < n-1; i++ {
		if got, want := reviseText(reloaded[i]), fmt.Sprintf("m%d", i); got != want {
			t.Fatalf("reloaded[%d] = %q, want %q (delete hit the wrong message)", i, got, want)
		}
	}
}

// TestReviseReanchorsResumeTrimmedEditOlder covers the older-message edit verb
// (a message-scoped variant) on a resumed-and-trimmed session: the replace amend
// must be persisted against the on-disk index so a reload shows the edit at the
// message the user changed, not one shifted by the trim offset.
func TestReviseReanchorsResumeTrimmedEditOlder(t *testing.T) {
	const n = 130
	s, path := seedResumedTrimmed(t, n)

	// Edit an in-memory message that is NOT in the current response span, so it
	// routes through editMessageAsVariant (an older-message edit). In-memory index
	// 10 == on-disk index 40; the last user turn is far later, so 10 is "older".
	const inMem = 10
	wantOnDisk := inMem + (n - 100) // 40
	if got := reviseText(s.agent.Messages()[inMem]); got != fmt.Sprintf("m%d", wantOnDisk) {
		t.Fatalf("in-memory[%d] = %q, want m%d", inMem, got, wantOnDisk)
	}
	if err := s.editMessage(s.agent.TranscriptEpoch(), inMem, "EDITED"); err != nil {
		t.Fatalf("edit: %v", err)
	}

	_, reloaded, err := core.OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reloaded) != n {
		t.Fatalf("reloaded length = %d, want %d", len(reloaded), n)
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("m%d", i)
		if i == wantOnDisk {
			want = "EDITED"
		}
		if got := reviseText(reloaded[i]); got != want {
			t.Fatalf("reloaded[%d] = %q, want %q (edit hit the wrong message)", i, got, want)
		}
	}
}
