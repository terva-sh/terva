package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The row must persist what a reader needs AND be invisible to resume — the
// same contract the stall and tail rows hold to.
func TestRetryRowPersistsAndIsSkippedByTheLoader(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "s.jsonl")
	sess, err := NewSessionAtPath(path, dir, "openai-codex", "m", "test")
	if err != nil {
		t.Fatalf("NewSessionAtPath: %v", err)
	}
	if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}}}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := sess.AppendRetry(core.RetryRecord{
		Phase: core.RetryPhaseCompaction, Provider: "openai-codex", Attempt: 3, Max: 6,
		Delay: 8 * time.Second, Err: "Our servers are currently overloaded.",
	}); err != nil {
		t.Fatalf("AppendRetry: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) == nil && row["type"] == "retry" {
			found, _ = row["retry"].(map[string]any)
		}
	}
	if found == nil {
		t.Fatal("no retry row written")
	}
	if found["phase"] != "compaction" {
		t.Errorf("phase = %v, want compaction", found["phase"])
	}
	if found["delay_ms"] != float64(8000) {
		t.Errorf("delay_ms = %v, want 8000", found["delay_ms"])
	}
	if found["attempt"] != float64(3) || found["max"] != float64(6) {
		t.Errorf("attempt/max = %v/%v, want 3/6", found["attempt"], found["max"])
	}

	// Resume must not see it. An informational row that reached the transcript
	// would be replayed to the model as content.
	msgs, err := ReadSessionMessages(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(msgs); got != 1 {
		t.Fatalf("resumed with %d messages, want 1 — the retry row entered the transcript", got)
	}
}
