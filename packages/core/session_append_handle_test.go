package core

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A session file has more than one writer. The live session holds a handle for
// its own rows, and RenameSession deliberately opens a SECOND handle to append
// a rename row — safe by design, "because it opens the file independently and
// appends".
//
// That is only true if the live handle appends too. Opened without O_APPEND it
// writes at its own offset, so the next flush lands ON TOP of anything appended
// behind its back and destroys it. The resume path had O_APPEND and the create
// path did not, so a session created in this run silently ate every manual
// rename while a resumed one kept them — which is what made it look like a
// display bug that only happened on active sessions.
//
// Asserted on the FILE, because the in-memory Session is not the thing that was
// wrong: it reported the right title over a file that had lost the row.
func TestANewSessionsHandleAppendsBehindAnExternalWriter(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "s.jsonl")

	s, err := NewSessionAtPath(path, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	// A session with no messages is PRUNED by Close, which would leave this
	// asserting against a zero-byte file and passing for the wrong reason.
	if err := s.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}

	// An external appender writes a row the live handle knows nothing about.
	if err := RenameSession(path, "Kept"); err != nil {
		t.Fatal(err)
	}
	// The live handle then writes its own. Under the bug this overwrites the row
	// above rather than following it.
	if err := s.UpdateReasoning("high"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	got := describeSession(path)
	if got.Title != "Kept" {
		t.Errorf("title = %q, want %q — the rename row was overwritten", got.Title, "Kept")
	}
	// The live handle's own row has to have survived too: an over-correction
	// that dropped its write would pass the check above and lose the other half.
	if got.Reasoning != "high" {
		t.Errorf("reasoning = %q, want %q — the live handle's own row was lost", got.Reasoning, "high")
	}
}
