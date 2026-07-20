package core

import (
	"context"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Rows written before the deferred-greeting stamp fix persist Time as the zero
// value, and session files are append-only — the zero is permanent on disk.
// Every reader must backfill it from a neighbor (the session's start for a
// leading row) so a resumed session, the replay rows, and the streaming
// inspector never surface a year-one timestamp.
func TestZeroTimeRowsBackfillOnLoad(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "p", "m", "test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	started := s.Meta.Started
	if started.IsZero() {
		t.Fatal("precondition: a new session records a start time")
	}
	// The old deferred-greeting bytes: an assistant row with the zero Time.
	greeting := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
		Meta:    map[string]string{MetaSource: "card:greeting"},
	}
	if err := s.AppendMessage(greeting); err != nil {
		t.Fatalf("append greeting: %v", err)
	}
	userAt := started.Add(42 * time.Second)
	if err := s.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
		Time:    userAt,
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// OpenSession: the loaded transcript carries the session start, not year one.
	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	if !msgs[0].Time.Equal(started) {
		t.Errorf("greeting time = %v, want the session start %v", msgs[0].Time, started)
	}
	if !msgs[1].Time.Equal(userAt) {
		t.Errorf("a real timestamp must never be touched: %v != %v", msgs[1].Time, userAt)
	}

	// ReadReplayRows: the player's first row must not open in year one.
	rows, meta, err := ReadReplayRows(path)
	if err != nil {
		t.Fatalf("replay rows: %v", err)
	}
	if !meta.Started.Equal(started) {
		t.Fatalf("replay meta started = %v, want %v", meta.Started, started)
	}
	if rows[0].Kind != ReplayRowMessage || !rows[0].Message.Time.Equal(started) {
		t.Errorf("replay row 0 time = %v, want %v", rows[0].Message.Time, started)
	}

	// StreamReplayMessages: the streaming twin backfills as rows pass.
	var streamed []time.Time
	if _, _, err := StreamReplayMessages(context.Background(), path, 0, func(_ int, m provider.Message) {
		streamed = append(streamed, m.Time)
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(streamed) != 2 || !streamed[0].Equal(started) || !streamed[1].Equal(userAt) {
		t.Errorf("streamed times = %v, want [%v %v]", streamed, started, userAt)
	}
}
