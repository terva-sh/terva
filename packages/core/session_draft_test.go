package core

import (
	"os"
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A deferred greeting writes nothing to disk (the file stays meta-only, so the
// prune gates treat the session as an empty draft) but still returns the active
// opening for the live transcript.
func TestDeferGreetingWritesNothing(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.DeferGreetingVariants([]provider.Message{
		variantMsg(provider.RoleAssistant, "g0"),
		variantMsg(provider.RoleAssistant, "g1"),
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if walkMsgTexts([]provider.Message{active})[0] != "g1" {
		t.Errorf("returned active = %v, want g1", walkMsgTexts([]provider.Message{active}))
	}
	if !s.HasPendingGreeting() {
		t.Error("HasPendingGreeting should be true after a defer")
	}
	if !sessionHasNoMessages(s.Path) {
		t.Error("a deferred greeting must leave the file with no message rows (a draft)")
	}
}

// Close prunes a never-flushed draft: a deferred greeting keeps messagesAppended
// at 0, so the fresh file is deleted on Close — the "opened a chat to preview,
// left without sending" cleanup.
func TestClosePrunesDeferredDraft(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeferGreetingVariants([]provider.Message{variantMsg(provider.RoleAssistant, "g0")}, 0); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a deferred-greeting draft should be pruned on Close, but the file survives: %v", err)
	}
}

// The first durable append flushes the deferred greeting BEFORE its own row, so
// the persisted transcript is greeting-then-content — identical to a seed-at-
// build — and the session is no longer a draft.
func TestFlushOnFirstAppend(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeferGreetingVariants([]provider.Message{
		variantMsg(provider.RoleAssistant, "g0"),
		variantMsg(provider.RoleAssistant, "g1"),
	}, 0); err != nil {
		t.Fatal(err)
	}
	// The first real turn's user message.
	if err := s.AppendMessage(variantMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatal(err)
	}
	if s.HasPendingGreeting() {
		t.Error("the greeting should have flushed on the first append")
	}
	path := s.Path
	_ = s.Close()

	eff, _, _, _ := walkTail(t, path)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"g0", "u0"}) {
		t.Errorf("effective = %v, want [g0 u0] (greeting before the user message)", got)
	}
}

// A pre-first-turn greeting swipe (SetPendingGreetingActive) is persisted at the
// flush: the chosen opening is the active one on reload, matching what the live
// transcript already showed.
func TestSwipeThenFlushPersistsActive(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeferGreetingVariants([]provider.Message{
		variantMsg(provider.RoleAssistant, "g0"),
		variantMsg(provider.RoleAssistant, "g1"),
		variantMsg(provider.RoleAssistant, "g2"),
	}, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SetPendingGreetingActive(2); !ok {
		t.Fatal("SetPendingGreetingActive should report a pending greeting")
	}
	if err := s.AppendMessage(variantMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	eff, _, _, _ := walkTail(t, path)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"g2", "u0"}) {
		t.Errorf("effective = %v, want [g2 u0] (the swiped opening persisted through the flush)", got)
	}
}

// flushPendingGreeting is idempotent and recursion-safe: a second flush is a
// no-op (SeedGreetingVariants re-enters the appenders, but the pending set is
// already nil), and the greeting is written exactly once.
func TestFlushIdempotent(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeferGreetingVariants([]provider.Message{
		variantMsg(provider.RoleAssistant, "g0"),
		variantMsg(provider.RoleAssistant, "g1"),
	}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.flushPendingGreeting(); err != nil {
		t.Fatal(err)
	}
	if s.HasPendingGreeting() {
		t.Error("HasPendingGreeting should be false after a flush")
	}
	if err := s.flushPendingGreeting(); err != nil { // no-op, must not error or re-write
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()
	eff, _, takes, _ := walkTail(t, path)
	if got := walkMsgTexts(eff); !reflect.DeepEqual(got, []string{"g0"}) {
		t.Errorf("effective = %v, want [g0] (written once)", got)
	}
	if len(takes) != 2 {
		t.Errorf("takes=%d, want 2 (the greeting written exactly once)", len(takes))
	}
}

// The flush is on every durable-content appender, not just AppendMessage: a
// compaction on a draft flushes the greeting first, so the opening reaches disk.
func TestCompactionFlushesDraft(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeferGreetingVariants([]provider.Message{variantMsg(provider.RoleAssistant, "g0")}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCompaction([]provider.Message{variantMsg(provider.RoleUser, "## summary")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	if s.HasPendingGreeting() {
		t.Error("AppendCompaction should have flushed the pending greeting")
	}
	if sessionHasNoMessages(s.Path) {
		t.Error("the greeting message row should be on disk after the compaction flush")
	}
}
