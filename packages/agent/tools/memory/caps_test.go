package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// boundProject returns a PROJECT store bound to a temp dir, plus that dir.
func boundProject(t *testing.T) (*Store, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	s := NewStore()
	if err := s.Rebind(dir); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// An over-length entry is REFUSED and nothing is stored. The old behaviour kept
// the first MaxEntryLen runes with "…" appended and reported success, which put
// three entries into a real corpus cut off mid-procedure — the half that said
// what to DO was the half that went.
func TestAnOverLongEntryIsRefusedNotShortened(t *testing.T) {
	s, _ := boundProject(t)
	long := strings.Repeat("x", MaxEntryLen+1)

	err := s.Add(long)
	if err == nil {
		t.Fatal("an over-length entry was accepted")
	}
	if !strings.Contains(err.Error(), "shorten") {
		t.Errorf("refusal should tell the caller what to do, got: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("refused entry was stored anyway: %d entr(ies)", len(got))
	}
	// The boundary itself must be accepted, or the limit is off by one.
	if err := s.Add(strings.Repeat("y", MaxEntryLen)); err != nil {
		t.Errorf("an entry of exactly MaxEntryLen was refused: %v", err)
	}
}

// Replace goes through the same gate: swapping a short entry for an over-long
// one must not quietly store a truncated replacement.
func TestReplaceRefusesAnOverLongReplacement(t *testing.T) {
	s, _ := boundProject(t)
	if err := s.Add("the build command is just ci"); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace("build command", strings.Repeat("z", MaxEntryLen+1)); err == nil {
		t.Fatal("an over-length replacement was accepted")
	}
	if got := s.List(); len(got) != 1 || !strings.Contains(got[0], "just ci") {
		t.Errorf("a refused replacement disturbed the stored entry: %v", got)
	}
}

// The file is markdown BECAUSE it is meant to be hand-edited. Applying the
// entry cap on the read path truncated on every load, so opening memory.md and
// writing a longer fact destroyed it at the next session start — silently, with
// no one present to be told. The cap belongs on the write path only.
func TestAHandEditedLongEntrySurvivesReload(t *testing.T) {
	s, dir := boundProject(t)
	long := strings.Repeat("q", MaxEntryLen*2)
	if err := os.WriteFile(filepath.Join(dir, projectFileName),
		[]byte(projectFileHeader+"\n- "+long+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if len(got[0]) != len(long) {
		t.Errorf("hand-edited entry was truncated on load: kept %d of %d bytes",
			len(got[0]), len(long))
	}
	if strings.HasSuffix(got[0], "…") {
		t.Error("entry came back with a truncation ellipsis")
	}
}

// blockMaxBytes is not an independent choice. It clamps the rendered block, so
// it must exceed what both scopes can hold plus the policy text — otherwise a
// full memory is silently truncated on its way into the prompt, which is the
// same defect the write path now refuses, one layer down where nothing can
// refuse it.
//
// Pins the RELATION rather than the number, so raising a file cap without
// raising this fails here instead of in a user's context window.
func TestTheBlockClampFitsBothScopesAtTheirCaps(t *testing.T) {
	need := MaxProjectBytes + MaxUserBytes + len(Policy())
	if blockMaxBytes < need {
		t.Errorf("blockMaxBytes = %d, but a full memory renders up to %d "+
			"(project %d + user %d + policy %d)",
			blockMaxBytes, need, MaxProjectBytes, MaxUserBytes, len(Policy()))
	}
}

// The count cap is documented as decorative — bytes bind long before it. If
// that ever stops being true the comment on MaxEntries is a lie, and a user
// would start seeing a refusal that names a limit the docs call a backstop.
func TestBytesBindBeforeTheEntryCount(t *testing.T) {
	maxByCount := MaxEntries * MaxEntryLen
	if maxByCount <= MaxProjectBytes {
		t.Errorf("MaxEntries (%d) x MaxEntryLen (%d) = %d now fits inside MaxProjectBytes (%d); "+
			"the count cap is no longer decorative and its comment needs updating",
			MaxEntries, MaxEntryLen, maxByCount, MaxProjectBytes)
	}
}
