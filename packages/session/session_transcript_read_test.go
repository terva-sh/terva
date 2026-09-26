package session

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func newTranscriptReadSession(t *testing.T) (*Session, string) {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/work", "p", "m", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	turn := provider.Usage{InputTokens: 80_000}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(turn, turn); err != nil {
		t.Fatal(err)
	}
	return s, path
}

// A clear landing between the two passes must not pair the old messages with
// the new, empty gauge. The read notices the file moved and reads it again,
// and both halves then describe the cleared session.
func TestReadSessionTranscriptRereadsAfterAWriteBetweenPasses(t *testing.T) {
	s, path := newTranscriptReadSession(t)
	cleared := false
	betweenTranscriptPasses = func() {
		if !cleared {
			cleared = true
			if err := s.AppendCompaction(nil, core.CompactResult{}); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { betweenTranscriptPasses = func() {} })

	tr, err := ReadSessionTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Messages) != 0 || tr.ResumeContext != (provider.Usage{}) {
		t.Errorf("got %d messages and gauge %+v; want the cleared session in both halves", len(tr.Messages), tr.ResumeContext)
	}
}

// A session that moves on every read is reported, not guessed at.
func TestReadSessionTranscriptGivesUpOnASessionThatKeepsMoving(t *testing.T) {
	s, path := newTranscriptReadSession(t)
	total := provider.Usage{InputTokens: 80_000}
	betweenTranscriptPasses = func() {
		turn := provider.Usage{InputTokens: 1}
		total = total.Add(turn)
		if err := s.AppendUsage(turn, total); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { betweenTranscriptPasses = func() {} })

	if _, err := ReadSessionTranscript(path); err == nil || !strings.Contains(err.Error(), "changed during each") {
		t.Errorf("err = %v, want the read to give up", err)
	}
}
