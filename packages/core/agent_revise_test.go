package core

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestAgentReviseOpsMatchReload is the replayed==live property across the seam
// that matters for Phase 1: the live Agent transcript after a chain of revise
// ops equals the transcript a fresh loader rebuilds from the persisted amend
// rows. Neither path repairs between amends, so indices stay consistent and the
// two converge.
func TestAgentReviseOpsMatchReload(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []provider.Message{revUser("m0"), revUser("m1"), revUser("m2"), revUser("m3")}
	for _, m := range msgs {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{}
	a.SetMessages(msgs)
	ep0 := a.TranscriptEpoch()

	// Each op mutates the live Agent and persists the matching amend.
	edited := revUser("edited")
	if !a.ReplaceMessage(1, edited) {
		t.Fatal("ReplaceMessage(1) returned false")
	}
	if err := s.AppendAmend(AmendReplace, 1, &edited, "edit"); err != nil {
		t.Fatal(err)
	}
	if !a.DeleteMessage(2) {
		t.Fatal("DeleteMessage(2) returned false")
	}
	if err := s.AppendAmend(AmendDelete, 2, nil, "delete"); err != nil {
		t.Fatal(err)
	}
	if !a.TruncateTo(2) {
		t.Fatal("TruncateTo(2) returned false")
	}
	if err := s.AppendAmend(AmendTruncate, 2, nil, "retry"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	_, reloaded, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	live := a.Messages()
	if !reflect.DeepEqual(walkMsgTexts(live), walkMsgTexts(reloaded)) {
		t.Errorf("live %v != reloaded %v", walkMsgTexts(live), walkMsgTexts(reloaded))
	}
	if want := []string{"m0", "edited"}; !reflect.DeepEqual(walkMsgTexts(live), want) {
		t.Errorf("transcript = %v, want %v", walkMsgTexts(live), want)
	}
	if a.TranscriptEpoch() == ep0 {
		t.Error("transcript epoch did not advance across the revise ops")
	}
}

// TestAgentReviseOpsBounds pins the out-of-range guards: a bad index is a no-op
// returning false and leaves the transcript untouched.
func TestAgentReviseOpsBounds(t *testing.T) {
	a := &Agent{}
	a.SetMessages([]provider.Message{revUser("only")})
	if a.ReplaceMessage(5, revUser("x")) || a.ReplaceMessage(-1, revUser("x")) {
		t.Error("ReplaceMessage out of range should return false")
	}
	if a.DeleteMessage(5) || a.DeleteMessage(-1) {
		t.Error("DeleteMessage out of range should return false")
	}
	if a.TruncateTo(5) || a.TruncateTo(-1) {
		t.Error("TruncateTo out of range should return false")
	}
	if !a.TruncateTo(1) { // == len is an accepted no-op
		t.Error("TruncateTo(len) should return true")
	}
	if got := walkMsgTexts(a.Messages()); !reflect.DeepEqual(got, []string{"only"}) {
		t.Errorf("transcript mutated by out-of-range ops: %v", got)
	}
}
