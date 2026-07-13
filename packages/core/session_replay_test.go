package core

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestReadReplayRows(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: "reply"},
			provider.ToolCallBlock{ID: "t1", Name: "read", Arguments: []byte(`{"path":"x"}`)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{
			provider.ToolResultBlock{CallID: "t1", Content: []provider.Content{provider.TextBlock{Text: "out"}}},
		}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}},
	}
	for _, m := range msgs {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.AppendUsage(provider.Usage{InputTokens: 10, OutputTokens: 5}, provider.Usage{InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}

	rows, meta, err := ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "model" || meta.Provider != "prov" {
		t.Errorf("meta = %+v", meta)
	}
	want := []ReplayRowKind{ReplayRowMessage, ReplayRowMessage, ReplayRowMessage, ReplayRowMessage, ReplayRowUsage}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, k := range want {
		if rows[i].Kind != k {
			t.Errorf("row %d kind = %s, want %s", i, rows[i].Kind, k)
		}
	}
	if rows[4].Usage.InputTokens != 10 {
		t.Errorf("usage input = %d, want 10", rows[4].Usage.InputTokens)
	}

	// StreamReplayMessages walks the same file with row-number parity: message
	// callbacks carry the index the row has in ReadReplayRows' slice, and the
	// usage row is counted (not delivered).
	var gotRows []int
	var gotFirstText []string
	smeta, truncated, err := StreamReplayMessages(context.Background(), path, 0, func(row int, m provider.Message) {
		gotRows = append(gotRows, row)
		if tb, ok := m.Content[0].(provider.TextBlock); ok {
			gotFirstText = append(gotFirstText, tb.Text)
		} else {
			gotFirstText = append(gotFirstText, "")
		}
	})
	if err != nil || truncated {
		t.Fatalf("stream: err=%v truncated=%v", err, truncated)
	}
	if smeta.Model != "model" || smeta.Provider != "prov" {
		t.Errorf("stream meta = %+v", smeta)
	}
	if len(gotRows) != 4 || gotRows[0] != 0 || gotRows[3] != 3 {
		t.Errorf("stream rows = %v, want [0 1 2 3]", gotRows)
	}
	if gotFirstText[0] != "hi" || gotFirstText[3] != "done" {
		t.Errorf("stream texts = %q", gotFirstText)
	}
}

// TestStreamReplayMessagesByteCeiling pins the input bound: the walk stops at
// the ceiling, reports truncated, and still delivers everything scanned so far.
func TestStreamReplayMessagesByteCeiling(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	for i := 0; i < 50; i++ {
		if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "0123456789012345678901234567890123456789"}}}); err != nil {
			t.Fatal(err)
		}
	}

	count := 0
	_, truncated, err := StreamReplayMessages(context.Background(), path, 2048, func(int, provider.Message) { count++ })
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("a 2 KiB ceiling over ~7 KiB of rows must report truncated")
	}
	if count == 0 || count >= 50 {
		t.Errorf("delivered %d messages, want a non-empty strict prefix", count)
	}

	// No ceiling (0) delivers everything.
	count = 0
	_, truncated, err = StreamReplayMessages(context.Background(), path, 0, func(int, provider.Message) { count++ })
	if err != nil || truncated || count != 50 {
		t.Errorf("unbounded walk = %d msgs, truncated=%v, err=%v; want 50, false, nil", count, truncated, err)
	}
}

// TestReadReplayRowsKeepsCompactedHistory is the whole point of the reader: it
// preserves the rows a compaction folded away, where OpenSession collapses them.
func TestReadReplayRowsKeepsCompactedHistory(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
	}
	if err := sess.AppendMessage(msg("one")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendMessage(msg("two")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCompaction([]provider.Message{msg("summary")}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendMessage(msg("three")); err != nil {
		t.Fatal(err)
	}

	rows, _, err := ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReplayRowKind{ReplayRowMessage, ReplayRowMessage, ReplayRowCompaction, ReplayRowMessage}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, k := range want {
		if rows[i].Kind != k {
			t.Errorf("row %d = %s, want %s", i, rows[i].Kind, k)
		}
	}
	if len(rows[2].Checkpoint) != 1 {
		t.Errorf("checkpoint len = %d, want 1", len(rows[2].Checkpoint))
	}

	// Contrast: the loader collapses to the checkpoint + the trailing message.
	_, loaded, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Errorf("OpenSession returned %d messages, want 2 (summary + three)", len(loaded))
	}
}

// TestForEachJSONLLineBoundedOversizedUnterminated pins Gap 1 at the read
// boundary: an oversized, UNTERMINATED trailing row is drained and skipped —
// never allocated whole or handed to fn — while the terminated small row before
// it still streams.
func TestForEachJSONLLineBoundedOversizedUnterminated(t *testing.T) {
	old := jsonlPerLineCeiling
	jsonlPerLineCeiling = 128
	defer func() { jsonlPerLineCeiling = old }()

	// A terminated small row, then a huge row with NO trailing newline.
	data := []byte(`{"type":"message"}` + "\n" + `{"big":"` + strings.Repeat("x", 8192) + `"}`)

	var got []string
	var oversizeBytes int64
	err := forEachJSONLLineBounded(bytes.NewReader(data), jsonlPerLineCeiling, 0,
		func(n int64) { oversizeBytes += n },
		func(line []byte) error { got = append(got, string(line)); return nil })
	if err != nil {
		t.Fatalf("bounded walk erred: %v", err)
	}
	if len(got) != 1 || got[0] != `{"type":"message"}` {
		t.Errorf("delivered %q, want only the small terminated row", got)
	}
	if oversizeBytes < 8192 {
		t.Errorf("onOversize saw %d bytes, want the full drained row (>8192)", oversizeBytes)
	}
}

// TestStreamReplayMessagesPerLineCeiling pins Gap 1 through the tool's entry
// point: one row past the per-line ceiling is skipped (flagging truncation)
// while the normal rows around it still stream.
func TestStreamReplayMessagesPerLineCeiling(t *testing.T) {
	old := jsonlPerLineCeiling
	jsonlPerLineCeiling = 512
	defer func() { jsonlPerLineCeiling = old }()

	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	small := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}
	big := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: strings.Repeat("x", 8192)}}}
	for _, m := range []provider.Message{small, big, small} {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	sess.Close()

	var texts []string
	_, truncated, err := StreamReplayMessages(context.Background(), path, 0, func(_ int, m provider.Message) {
		if tb, ok := m.Content[0].(provider.TextBlock); ok {
			texts = append(texts, tb.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("an oversized row must flag truncated")
	}
	if len(texts) != 2 || texts[0] != "hello" || texts[1] != "hello" {
		t.Errorf("delivered %q, want the two small rows only (oversized row skipped, not hydrated)", texts)
	}
}

// TestStreamReplayMessagesCancellation pins the Gap 3 ctx check: a scan aborts
// promptly on cancellation and surfaces the error rather than walking on.
func TestStreamReplayMessagesCancellation(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "p", "m", "v")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "x"}}}); err != nil {
			t.Fatal(err)
		}
	}
	sess.Close()

	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	_, _, err = StreamReplayMessages(ctx, path, 0, func(int, provider.Message) {
		count++
		if count == 3 {
			cancel() // cancel mid-scan
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if count >= 100 {
		t.Errorf("scan delivered all %d rows despite cancellation", count)
	}
}
