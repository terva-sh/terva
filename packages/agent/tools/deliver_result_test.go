package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/deliverable"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

const drSchema = `{"type":"object","required":["count"],"properties":{"count":{"type":"integer"}},"additionalProperties":false}`

func TestDeliverResultWritesValidatedDocument(t *testing.T) {
	dir := testsupport.TempDir(t)
	tool := &DeliverResultTool{ArgSchema: json.RawMessage(drSchema), Dir: dir}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"count": 3}`), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res.Content)
	}
	raw, err := os.ReadFile(filepath.Join(dir, deliverable.FileName))
	if err != nil {
		t.Fatalf("deliverable file: %v", err)
	}
	if string(raw) != `{"count": 3}` {
		t.Fatalf("file content = %s", raw)
	}
}

func TestDeliverResultRejectsSchemaMismatchForRetry(t *testing.T) {
	dir := testsupport.TempDir(t)
	tool := &DeliverResultTool{ArgSchema: json.RawMessage(drSchema), Dir: dir}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"count": "three"}`), nil)
	if err != nil {
		t.Fatalf("a validation miss must be a retryable tool RESULT, not a hard error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error result")
	}
	var text string
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			text += tb.Text
		}
	}
	if !strings.Contains(text, "$.count") || !strings.Contains(text, "call deliver_result again") {
		t.Fatalf("rejection must carry the path and the retry instruction: %q", text)
	}
	if _, err := os.Stat(filepath.Join(dir, deliverable.FileName)); err == nil {
		t.Fatal("a rejected deliverable must not be written")
	}
}

func TestDeliverResultOutsideSwarmChildFailsClosed(t *testing.T) {
	t.Setenv("TERVA_SWARM_EVENT_LOG", "")
	tool := &DeliverResultTool{ArgSchema: json.RawMessage(drSchema)}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"count": 1}`), nil); err == nil || !strings.Contains(err.Error(), "swarm") {
		t.Fatalf("err = %v, want no-state-dir failure", err)
	}
}
