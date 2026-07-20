package core

import (
	"os"
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func walkMsgText(m provider.Message) string {
	if len(m.Content) == 0 {
		return ""
	}
	tb, _ := m.Content[0].(provider.TextBlock)
	return tb.Text
}

func walkMsgTexts(ms []provider.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = walkMsgText(m)
	}
	return out
}

// TestWalkSessionFoldMatchesOpenSession pins the shared walker's effective
// transcript against OpenSession over a session with a compaction checkpoint.
// The fold — append a message row, reset on a checkpoint — is the seam every
// reader now shares (and the one an amend row will extend), so the two readers
// must reconstruct the same transcript, and the hooks must observe rows and the
// compaction boundary in file order with the right indices.
func TestWalkSessionFoldMatchesOpenSession(t *testing.T) {
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
	// Checkpoint keeps [sum1, m2]; two more turns follow it.
	if err := s.AppendCompaction([]provider.Message{revSummary("sum1"), revUser("m2")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{revUser("m3"), revUser("m4")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	path := s.Path
	_ = s.Close()

	// OpenSession is the reference reconstruction.
	_, want, err := OpenSession(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var msgIdx []int
	var compOrdinals, beforeLens []int
	got, err := walkSession(f, &loadReport{}, sessionWalkHooks{
		onMessage: func(_ provider.Message, idx int, _ []byte) { msgIdx = append(msgIdx, idx) },
		onCompaction: func(_, before []provider.Message, ord int, _ []byte) {
			compOrdinals = append(compOrdinals, ord)
			beforeLens = append(beforeLens, len(before))
		},
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if g, w := walkMsgTexts(got), walkMsgTexts(want); !reflect.DeepEqual(g, w) {
		t.Errorf("effective transcript = %v, want %v (OpenSession)", g, w)
	}
	if w := []string{"sum1", "m2", "m3", "m4"}; !reflect.DeepEqual(walkMsgTexts(got), w) {
		t.Errorf("effective transcript = %v, want %v", walkMsgTexts(got), w)
	}
	// Five message rows: m0,m1,m2 (pre-checkpoint), m3,m4 (post-reset). The idx is
	// the position in the effective transcript after the append; the checkpoint
	// reset to len 2 makes m3 land at idx 2 again.
	if w := []int{0, 1, 2, 2, 3}; !reflect.DeepEqual(msgIdx, w) {
		t.Errorf("onMessage idx sequence = %v, want %v", msgIdx, w)
	}
	// One checkpoint, ordinal 0, summarizing the 3 messages before it.
	if !reflect.DeepEqual(compOrdinals, []int{0}) || !reflect.DeepEqual(beforeLens, []int{3}) {
		t.Errorf("compaction hooks: ordinals=%v beforeLens=%v, want [0] and [3]", compOrdinals, beforeLens)
	}
}

// TestWalkSessionDropsEmptyMessages mirrors the loader: a message row whose
// content is empty never enters the effective transcript and fires no onMessage.
func TestWalkSessionDropsEmptyMessages(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(revUser("keep")); err != nil {
		t.Fatal(err)
	}
	// An empty-content message: the loader drops it, so the walker must too.
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(revUser("keep2")); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var seen int
	got, err := walkSession(f, &loadReport{}, sessionWalkHooks{
		onMessage: func(_ provider.Message, _ int, _ []byte) { seen++ },
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if w := []string{"keep", "keep2"}; !reflect.DeepEqual(walkMsgTexts(got), w) {
		t.Errorf("effective = %v, want %v", walkMsgTexts(got), w)
	}
	if seen != 2 {
		t.Errorf("onMessage fired %d times, want 2 (empty message dropped)", seen)
	}
}

// TestSetCreationSpecRoundTrips pins the per-session creation spec (persona plus
// the immersive fields) through a write + reload: a resumed session recovers the
// persona and Stage parameters instead of falling back to the workspace default.
func TestSetCreationSpecRoundTrips(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCreationSpec("kertoja", "play", "cards/aava", map[string]string{"aava": "cards/aava"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(revUser("hi")); err != nil { // keep the session from being pruned on Close
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	s2, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	m := s2.Meta
	if m.Persona != "kertoja" || m.Experience != "play" || m.Card != "cards/aava" || m.Greeting != 2 || m.Cast["aava"] != "cards/aava" {
		t.Errorf("creation spec did not round-trip: %+v", m)
	}
}

// TestSetNoteRoundTrips pins the author's-note meta field: last-wins across
// repeated writes, durable across a reopen, and clearable back to empty.
func TestSetNoteRoundTrips(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetNote("stay in character; it is raining"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNote("stay in character; the storm has passed"); err != nil { // last wins
		t.Fatal(err)
	}
	if err := s.AppendMessage(revUser("hi")); err != nil { // keep the session from being pruned on Close
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	s2, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.Meta.Note; got != "stay in character; the storm has passed" {
		t.Errorf("note did not round-trip (last-wins): %q", got)
	}
	if err := s2.SetNote(""); err != nil { // clear
		t.Fatal(err)
	}
	_ = s2.Close()

	s3, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen after clear: %v", err)
	}
	defer s3.Close()
	if s3.Meta.Note != "" {
		t.Errorf("cleared note did not persist empty: %q", s3.Meta.Note)
	}
}

func TestSetUserPersonaRoundTrips(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserPersona("Kir", "a nervous apprentice", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserPersona("Kira", "a seasoned courier who trusts no one", "woman", "she/her"); err != nil { // last wins
		t.Fatal(err)
	}
	if err := s.AppendMessage(revUser("hi")); err != nil { // keep the session from being pruned on Close
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	s2, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.Meta.UserName; got != "Kira" {
		t.Errorf("user name did not round-trip (last-wins): %q", got)
	}
	if got := s2.Meta.UserDescription; got != "a seasoned courier who trusts no one" {
		t.Errorf("user description did not round-trip (last-wins): %q", got)
	}
	if got := s2.Meta.UserGender; got != "woman" {
		t.Errorf("user gender did not round-trip (last-wins): %q", got)
	}
	if got := s2.Meta.UserPronouns; got != "she/her" {
		t.Errorf("user pronouns did not round-trip (last-wins): %q", got)
	}
	if err := s2.SetUserPersona("", "", "", ""); err != nil { // clear all halves
		t.Fatal(err)
	}
	_ = s2.Close()

	s3, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen after clear: %v", err)
	}
	defer s3.Close()
	if s3.Meta.UserName != "" || s3.Meta.UserDescription != "" {
		t.Errorf("cleared user persona did not persist empty: name=%q desc=%q", s3.Meta.UserName, s3.Meta.UserDescription)
	}
	if s3.Meta.UserGender != "" || s3.Meta.UserPronouns != "" {
		t.Errorf("cleared gender/pronouns did not persist empty: gender=%q pronouns=%q", s3.Meta.UserGender, s3.Meta.UserPronouns)
	}
}
