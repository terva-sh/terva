package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// TestSessionRoundTripAllBlockKinds pins format v2: every content
// block kind survives append → close → open unchanged, and the file
// carries explicit type discriminators plus the format version.
func TestSessionRoundTripAllBlockKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-compatible", "test-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}

	when := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	assistant := provider.Message{
		Role: provider.RoleAssistant,
		Time: when,
		Content: []provider.Content{
			provider.TextBlock{Text: "let me check"},
			provider.ReasoningBlock{ID: "r1", Summary: "thinking", Encrypted: "blob"},
			provider.ToolCallBlock{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		},
	}
	toolMsg := provider.Message{
		Role: provider.RoleTool,
		Time: when,
		Content: []provider.Content{
			provider.ToolResultBlock{
				CallID:  "call_1",
				IsError: false,
				Content: []provider.Content{
					provider.TextBlock{Text: "file contents"},
					provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}},
				},
			},
		},
	}
	user := provider.Message{
		Role:    provider.RoleUser,
		Time:    when,
		Content: []provider.Content{provider.TextBlock{Text: "read a.txt"}},
	}
	for _, m := range []provider.Message{user, assistant, toolMsg} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The on-disk rows carry discriminators and the format version.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"format_version":2`, `"type":"text"`, `"type":"reasoning"`,
		`"type":"tool_call"`, `"type":"tool_result"`, `"type":"image"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("session file missing %s", want)
		}
	}

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.LoadWarnings) != 0 {
		t.Errorf("clean v2 file produced warnings: %v", reopened.LoadWarnings)
	}
	if reopened.Meta.FormatVersion != sessionFormatVersion {
		t.Errorf("FormatVersion = %d, want %d", reopened.Meta.FormatVersion, sessionFormatVersion)
	}
	want := []provider.Message{user, assistant, toolMsg}
	if !reflect.DeepEqual(msgs, want) {
		t.Errorf("round trip mismatch:\ngot:  %#v\nwant: %#v", msgs, want)
	}
}

// TestSessionCompactionRoundTrip: compaction rows use the same typed
// encoding and replace earlier rows on load.
func TestSessionCompactionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	pre := provider.Message{Role: provider.RoleUser, Time: when,
		Content: []provider.Content{provider.TextBlock{Text: "old turn"}}}
	if err := s.AppendMessage(pre); err != nil {
		t.Fatal(err)
	}
	summary := provider.Message{Role: provider.RoleAssistant, Time: when,
		Content: []provider.Content{provider.TextBlock{Text: "summary of everything"}}}
	if err := s.AppendCompaction([]provider.Message{summary}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	// OpenSession reopens the file for appending; windows can't remove
	// the TempDir while that handle is live.
	defer reopened.Close()
	if len(msgs) != 1 || !reflect.DeepEqual(msgs[0], summary) {
		t.Errorf("compaction load mismatch: %#v", msgs)
	}
}

// writeRawSession writes a session file from raw JSONL lines.
func writeRawSession(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSessionV1LegacyFileLoads: pre-discriminator files (no
// format_version, untyped blocks) hydrate through the field-presence
// fallback, warning-free.
func TestSessionV1LegacyFileLoads(t *testing.T) {
	path := writeRawSession(t,
		`{"type":"meta","meta":{"id":"x","cwd":"/ws","model":"m","provider":"p","started":"2026-06-10T12:00:00Z","version":"0.2.0"}}`,
		`{"type":"message","message":{"role":"user","content":[{"text":"hi"}],"time":"2026-06-10T12:00:00Z"}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"id":"c1","name":"read","arguments":{"path":"a"}}],"time":"2026-06-10T12:00:01Z"}}`,
		`{"type":"message","message":{"role":"tool","content":[{"call_id":"c1","content":[{"text":"ok"}],"is_error":false}],"time":"2026-06-10T12:00:02Z"}}`,
	)
	s, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.LoadWarnings) != 0 {
		t.Errorf("legacy file produced warnings: %v", s.LoadWarnings)
	}
	if len(msgs) != 3 {
		t.Fatalf("loaded %d messages, want 3", len(msgs))
	}
	if _, ok := msgs[1].Content[0].(provider.ToolCallBlock); !ok {
		t.Errorf("legacy tool_call block hydrated as %T", msgs[1].Content[0])
	}
	if _, ok := msgs[2].Content[0].(provider.ToolResultBlock); !ok {
		t.Errorf("legacy tool_result block hydrated as %T", msgs[2].Content[0])
	}
}

// TestSessionUnknownTypedBlockSkippedWithWarning: a block written by
// a future terva ("type" we don't know) is skipped and reported — NOT
// degraded to an empty text block.
func TestSessionUnknownTypedBlockSkippedWithWarning(t *testing.T) {
	path := writeRawSession(t,
		`{"type":"meta","meta":{"id":"x","cwd":"/ws","model":"m","provider":"p","started":"2026-06-10T12:00:00Z","version":"9.9.9","format_version":2}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"hologram","frames":3},{"type":"text","text":"visible"}],"time":"2026-06-10T12:00:00Z"}}`,
	)
	s, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(msgs) != 1 || len(msgs[0].Content) != 1 {
		t.Fatalf("want 1 message with 1 surviving block, got %#v", msgs)
	}
	if tb, ok := msgs[0].Content[0].(provider.TextBlock); !ok || tb.Text != "visible" {
		t.Errorf("surviving block = %#v, want the text block", msgs[0].Content[0])
	}
	found := false
	for _, w := range s.LoadWarnings {
		if strings.Contains(w, "hologram") {
			found = true
		}
	}
	if !found {
		t.Errorf("unknown block type not reported; warnings: %v", s.LoadWarnings)
	}
}

// TestSessionCorruptLineCounted: a garbage line no longer disappears
// silently — the rest loads and the skip is reported.
func TestSessionCorruptLineCounted(t *testing.T) {
	path := writeRawSession(t,
		`{"type":"meta","meta":{"id":"x","cwd":"/ws","model":"m","provider":"p","started":"2026-06-10T12:00:00Z","version":"0.2.0","format_version":2}}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"first"}],"time":"2026-06-10T12:00:00Z"}}`,
		`{not json at all`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"second"}],"time":"2026-06-10T12:00:01Z"}}`,
	)
	s, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(msgs) != 2 {
		t.Fatalf("loaded %d messages, want 2", len(msgs))
	}
	found := false
	for _, w := range s.LoadWarnings {
		if strings.Contains(w, "corrupt line") {
			found = true
		}
	}
	if !found {
		t.Errorf("corrupt line not reported; warnings: %v", s.LoadWarnings)
	}
}

// TestSessionNewerFormatWarns: a file declaring a future format
// version loads best-effort with an explicit warning.
func TestSessionNewerFormatWarns(t *testing.T) {
	path := writeRawSession(t,
		`{"type":"meta","meta":{"id":"x","cwd":"/ws","model":"m","provider":"p","started":"2026-06-10T12:00:00Z","version":"9.9.9","format_version":99}}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}],"time":"2026-06-10T12:00:00Z"}}`,
	)
	s, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(msgs) != 1 {
		t.Fatalf("loaded %d messages, want 1", len(msgs))
	}
	found := false
	for _, w := range s.LoadWarnings {
		if strings.Contains(w, "newer terva") && strings.Contains(w, "v99") {
			found = true
		}
	}
	if !found {
		t.Errorf("newer format not reported; warnings: %v", s.LoadWarnings)
	}
}
