package swarm

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// lastAssistantOf reads the agent's recovered last answer under its lock.
func lastAssistantOf(a *Agent) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastAssistant
}

// wireMessageEvent builds an assistant_message event the way the serializer
// actually writes one — nested under "message" — by going through
// core.WireEvent rather than by hand.
//
// Building it by hand is what hid the bug. Every existing replay test wrote the
// LEGACY flat shape, so all of them passed against a replay path that could read
// only the legacy flat shape, while the current serializer had long since moved
// to the nested one.
func wireMessageEvent(t *testing.T, text string) Event {
	t.Helper()
	we := core.WireEvent{
		Type: "assistant_message",
		Message: &core.WireMessage{
			Role:    string(provider.RoleAssistant),
			Content: []core.WireBlock{{Type: "text", Text: text}},
		},
	}
	b, err := json.Marshal(we)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if _, nested := data["message"]; !nested {
		t.Fatalf("the serializer no longer nests message content; this test's premise is stale: %v", data)
	}
	return Event{Type: "assistant_message", Data: data}
}

// replayEventsIntoAgent read message content only from the LEGACY flat `content`
// key, while its live twin applyEventToSink had been taught the canonical nested
// shape. So replaying a CURRENT events.jsonl recovered nothing: a detached agent
// came back with an empty transcript and no last answer, which is the entire
// point of the replay.
func TestReplayReadsTheCanonicalNestedMessageShape(t *testing.T) {
	a := &Agent{ID: "a1"}
	replayEventsIntoAgent(a, []Event{wireMessageEvent(t, "the finding")})

	if got := lastAssistantOf(a); !strings.Contains(got, "the finding") {
		t.Errorf("last assistant after replay = %q, want it to contain %q — "+
			"the replay path is reading a message shape the serializer no longer writes",
			got, "the finding")
	}
	if tr := strings.Join(a.Transcript(), "\n"); !strings.Contains(tr, "the finding") {
		t.Errorf("transcript after replay = %q, want it to contain the assistant text", tr)
	}
}

// The legacy flat shape must keep replaying: old events.jsonl files on disk
// predate the serializer unification and are not rewritten.
func TestReplayStillReadsTheLegacyFlatMessageShape(t *testing.T) {
	a := &Agent{ID: "a1"}
	replayEventsIntoAgent(a, []Event{{
		Type: "assistant_message",
		Data: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "old answer"}},
		},
	}})

	if got := lastAssistantOf(a); !strings.Contains(got, "old answer") {
		t.Errorf("last assistant = %q, want the legacy flat shape to still replay", got)
	}
}

// The live sink and the replay path are two readers of one log. Whatever shape
// arrives, they must recover the same text — that agreement is the property, and
// it is what a single shared reader buys.
func TestLiveSinkAndReplayAgreeOnBothShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   Event
	}{
		{"nested", wireMessageEvent(t, "shared text")},
		{"flat", Event{Type: "assistant_message", Data: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "shared text"}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := eventMessageContent(tc.ev.Data)
			if !ok || len(c) == 0 {
				t.Fatalf("the shared reader found no content in the %s shape", tc.name)
			}
			a := &Agent{ID: "a1"}
			replayEventsIntoAgent(a, []Event{tc.ev})
			if got := lastAssistantOf(a); !strings.Contains(got, "shared text") {
				t.Errorf("replay of the %s shape lost the text: %q", tc.name, got)
			}
		})
	}
}
