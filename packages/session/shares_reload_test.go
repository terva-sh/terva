package session

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The point of hanging the record on the message: reopen the session days later
// and the cards are still there, in the right places, with nothing to join.
func TestSharesSurviveASessionReload(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "anthropic", "claude", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	shared := []core.SharedFile{{ID: "shr_a", CallID: "call_1", Name: "report.pdf", Kind: "document", Size: 9}}
	raw, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{
		Role:    provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{CallID: "call_1"}},
		Meta:    map[string]string{core.MetaShared: string(raw)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	var found []core.SharedFile
	for _, m := range msgs {
		found = append(found, core.MessageToWire(m).Shared...)
	}
	if len(found) != 1 || found[0].ID != "shr_a" || found[0].CallID != "call_1" {
		t.Fatalf("shares after reload = %+v, want the one that was recorded", found)
	}
}
