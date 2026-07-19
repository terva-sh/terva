package core

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestAmendReplaceDeleteTruncate is the Phase-1 acceptance property in the
// session-format layer: a reloaded transcript equals what an in-order
// application of the amend chain produces. replace/delete/truncate are applied
// against the effective transcript at the point each row lands, so the reader
// and a would-be writer agree by construction.
func TestAmendReplaceDeleteTruncate(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{revUser("m0"), revUser("m1"), revUser("m2"), revUser("m3")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// [m0,m1,m2,m3] -> replace 1 -> [m0,edited,m2,m3]
	edited := revUser("edited")
	if err := s.AppendAmend(AmendReplace, 1, &edited, "edit"); err != nil {
		t.Fatal(err)
	}
	// -> delete 2 -> [m0,edited,m3]
	if err := s.AppendAmend(AmendDelete, 2, nil, "delete"); err != nil {
		t.Fatal(err)
	}
	// -> truncate 2 -> [m0,edited]
	if err := s.AppendAmend(AmendTruncate, 2, nil, "retry"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	_, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, want := walkMsgTexts(msgs), []string{"m0", "edited"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reloaded transcript = %v, want %v", got, want)
	}
}

// TestAmendAcrossCompaction proves a compaction absorbs prior amends (it resets
// the effective transcript to its stored output) while amends that follow it
// apply to that reset transcript — the "compaction collapses amend history" rule.
func TestAmendAcrossCompaction(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{revUser("m0"), revUser("m1"), revUser("m2")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// A pre-compaction edit that the checkpoint will absorb.
	edited0 := revUser("edited0")
	if err := s.AppendAmend(AmendReplace, 0, &edited0, "edit"); err != nil {
		t.Fatal(err)
	}
	// Checkpoint resets the effective transcript to [sum, m2].
	if err := s.AppendCompaction([]provider.Message{revSummary("sum"), revUser("m2")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	// A post-compaction amend applies to the reset transcript: delete sum.
	if err := s.AppendAmend(AmendDelete, 0, nil, "delete"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	_, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, want := walkMsgTexts(msgs), []string{"m2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reloaded transcript = %v, want %v (pre-compaction edit absorbed, post applied)", got, want)
	}
}

// TestBranchSessionWithAmends proves a fork of an amended session re-encodes from
// the effective (amended) transcript rather than copying stale raw rows — the
// disabled verbatim-copy fast path.
func TestBranchSessionWithAmends(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{revUser("m0"), revUser("m1"), revUser("m2")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	edited1 := revUser("edited1")
	if err := s.AppendAmend(AmendReplace, 1, &edited1, "edit"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	// Fork the first two messages: [m0, edited1] — NOT [m0, m1].
	branchPath, err := BranchSession(path, dir, dir, "v", 2)
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	_, msgs, err := OpenSession(branchPath)
	if err != nil {
		t.Fatalf("open branch: %v", err)
	}
	if got, want := walkMsgTexts(msgs), []string{"m0", "edited1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("branch transcript = %v, want %v (amended prefix)", got, want)
	}
}

// TestAmendBumpsFormatVersion pins the downgrade-safety guard: a session that
// never revises stays at the base version (a pre-amend build reads it without
// complaint), the first amend lifts it to the amend-aware version via one extra
// meta row, and further amends add no more (idempotent). A v3 file loads on this
// build warning-free.
func TestAmendBumpsFormatVersion(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(revUser("m0")); err != nil {
		t.Fatal(err)
	}
	// Before any amend the session declares the base version.
	if pre := metaRows(t, s.Path); pre[len(pre)-1].FormatVersion != sessionFormatVersion {
		t.Fatalf("pre-amend format = %d, want %d", pre[len(pre)-1].FormatVersion, sessionFormatVersion)
	}
	edited := revUser("m0-edited")
	if err := s.AppendAmend(AmendReplace, 0, &edited, "edit"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAmend(AmendReplace, 0, &edited, "edit again"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Exactly one bump meta row (create + one bump), no matter how many amends.
	if rows := metaRows(t, path); len(rows) != 2 {
		t.Errorf("got %d meta rows, want 2 (create + one format bump)", len(rows))
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Meta.FormatVersion != sessionFormatVersionAmend {
		t.Errorf("reloaded FormatVersion = %d, want %d", reopened.Meta.FormatVersion, sessionFormatVersionAmend)
	}
	if len(reopened.LoadWarnings) != 0 {
		t.Errorf("a v%d file must load this build warning-free: %v", sessionFormatVersionAmend, reopened.LoadWarnings)
	}
}
