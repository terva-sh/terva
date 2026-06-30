package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// TestRefreshToolPathsCache covers the memoised refreshToolPaths: it must
// derive path/offset/label correctly, stay consistent with the public
// ShortArgs, re-parse when a call's arguments change (streaming), and
// prune entries for calls that leave the transcript.
func TestRefreshToolPathsCache(t *testing.T) {
	v := &View{Theme: Dark}
	setArgs := func(args string) {
		v.Messages = []provider.Message{{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID:        "tc1",
				Name:      "read",
				Arguments: json.RawMessage(args),
			}},
		}}
	}

	setArgs(`{"path":"a.go","offset":5,"limit":10}`)
	v.refreshToolPaths()
	if got := v.toolPaths["tc1"]; got != "a.go" {
		t.Fatalf("path = %q, want a.go", got)
	}
	if got := v.toolStartLines["tc1"]; got != 5 {
		t.Fatalf("offset = %d, want 5", got)
	}
	// The single-parse label must match the three-parse public helper.
	if want := "read " + ShortArgs("read", json.RawMessage(`{"path":"a.go","offset":5,"limit":10}`)); v.toolCallLabels["tc1"] != want {
		t.Fatalf("label = %q, want %q", v.toolCallLabels["tc1"], want)
	}
	if !strings.HasPrefix(v.toolCallLabels["tc1"], "read ") {
		t.Fatalf("label = %q, want read prefix", v.toolCallLabels["tc1"])
	}

	// Changed args (a streaming call growing, or a different call) must
	// invalidate the cached parse rather than serve stale path/offset.
	setArgs(`{"path":"bb.go","offset":9}`)
	v.refreshToolPaths()
	if got := v.toolPaths["tc1"]; got != "bb.go" {
		t.Fatalf("after change path = %q, want bb.go (stale cache)", got)
	}
	if got := v.toolStartLines["tc1"]; got != 9 {
		t.Fatalf("after change offset = %d, want 9", got)
	}

	// A call no longer in the transcript is pruned from the cache.
	v.Messages = nil
	v.refreshToolPaths()
	if _, ok := v.toolArgCache["tc1"]; ok {
		t.Fatalf("cache entry not pruned after call left transcript")
	}
}

// TestHashMessageToolResult verifies that hashMessage keys tool results
// by (CallID, length) rather than body content: distinct results hash
// differently, a result that grows (streaming) re-hashes, but a
// same-length body swap is intentionally treated as unchanged.
func TestHashMessageToolResult(t *testing.T) {
	result := func(callID, body string, isErr bool) provider.Message {
		return provider.Message{
			Role: provider.RoleTool,
			Content: []provider.Content{provider.ToolResultBlock{
				CallID:  callID,
				IsError: isErr,
				Content: []provider.Content{provider.TextBlock{Text: body}},
			}},
		}
	}

	base := hashMessage(result("c1", "hello world", false))

	// Different CallID -> different hash.
	if hashMessage(result("c2", "hello world", false)) == base {
		t.Fatal("different CallID should change the hash")
	}
	// Growing body (length changes) -> different hash, so a streaming
	// result re-renders as it fills in.
	if hashMessage(result("c1", "hello world!!", false)) == base {
		t.Fatal("longer body should change the hash")
	}
	// IsError flips -> different hash (error styling differs).
	if hashMessage(result("c1", "hello world", true)) == base {
		t.Fatal("IsError should change the hash")
	}
	// Same CallID + same length, different content -> deliberately equal
	// (the documented trade-off: real tool output never does this).
	if hashMessage(result("c1", "HELLO WORLD", false)) != base {
		t.Fatal("same-length body swap is expected to hash equal")
	}
}
