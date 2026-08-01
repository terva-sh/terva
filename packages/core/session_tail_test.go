package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A tail row round-trips to disk and is skipped by the loader: the rebuilt
// transcript holds only the real messages, so recording what the harness
// injected never perturbs resume. The same forward-compat property the stall and
// escalation rows have, and for the same reason — the row-type switch has no
// default, so a loader that predates the row ignores it.
func TestTailRowRoundTripsAndIsSkippedOnResume(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-compatible", "gemma-4-26b", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "do the thing"}}}))
	must(s.AppendTail(TailRecord{Blocks: []TailBlock{
		{ID: TailHost, Text: "the live task card"},
		{ID: TailCapabilityFull, Text: "[inactive tool groups] mail: mail_send"},
	}}))
	must(s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}}))
	must(s.Close())

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	// OpenSession reopens the file for appending; Windows can't remove the
	// TempDir while that handle is live.
	defer reopened.Close()
	if len(msgs) != 2 {
		t.Fatalf("tail row must not enter the transcript: want 2 messages, got %d", len(msgs))
	}

	rows := readTailRows(t, path)
	if len(rows) != 1 {
		t.Fatalf("want one tail row, got %d", len(rows))
	}
	blocks := rows[0].Blocks
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	if blocks[0].ID != TailHost || blocks[1].ID != TailCapabilityFull {
		t.Errorf("block order lost: %q then %q", blocks[0].ID, blocks[1].ID)
	}
	// The text is the point: a size alone says a note fired and nothing about
	// what it said, and the review this row exists for turned on wording.
	if !strings.Contains(blocks[1].Text, "mail_send") {
		t.Errorf("the row did not carry the note's text: %q", blocks[1].Text)
	}
	if blocks[0].Bytes != len("the live task card") {
		t.Errorf("bytes = %d, want %d", blocks[0].Bytes, len("the live task card"))
	}
}

// An empty composition is a meaningful row — it is what ends the previous one —
// so it must encode as [] and not null. A reader has to be able to tell "the
// tail became empty here" from a row whose payload failed to encode.
func TestEmptyTailRowEncodesAsAnEmptyList(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTail(TailRecord{}); err != nil {
		t.Fatal(err)
	}
	// A session with no messages is discarded on Close, tail rows or not — a
	// conversation where nothing was said is not worth keeping.
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"blocks":[]`) {
		t.Errorf("empty tail row did not encode as an empty list:\n%s", b)
	}
}

// Truncation must not make the row lie about size. Bytes is what the model was
// actually shown (and charged for), which is most of what a reader comes to this
// row to learn; clipping the text and then reporting the clipped length would
// quietly understate every large host block.
func TestTruncatedTailBlockStillReportsItsTrueSize(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", tailTextCap+5000)
	if err := s.AppendTail(TailRecord{Blocks: []TailBlock{{ID: TailHost, Text: big}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	rows := readTailRows(t, path)
	if len(rows) != 1 || len(rows[0].Blocks) != 1 {
		t.Fatalf("want one row with one block, got %+v", rows)
	}
	b := rows[0].Blocks[0]
	if b.Bytes != len(big) {
		t.Errorf("bytes = %d, want the true size %d", b.Bytes, len(big))
	}
	if len(b.Text) != tailTextCap {
		t.Errorf("text len = %d, want it clipped to %d", len(b.Text), tailTextCap)
	}
	if !b.Truncated {
		t.Error("a clipped block must say so, or the text reads as complete")
	}
}

// A nil session tolerates AppendTail, like every other Append.
func TestAppendTailOnNilSession(t *testing.T) {
	var s *Session
	if err := s.AppendTail(TailRecord{Blocks: []TailBlock{{ID: TailHost, Text: "x"}}}); err != nil {
		t.Errorf("AppendTail on a nil session must be a no-op, got %v", err)
	}
}

func readTailRows(t *testing.T, path string) []tailRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var found []tailRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var row sessionLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == recordTail && row.Tail != nil {
			found = append(found, *row.Tail)
		}
	}
	return found
}
