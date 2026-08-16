package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// sharingTool publishes whatever it was configured with, the way share_file
// does: a text line for the model, and the record alongside it.
type sharingTool struct {
	files []SharedFile
}

func (t *sharingTool) Name() string            { return "share_file" }
func (t *sharingTool) Description() string     { return "share a file with the user" }
func (t *sharingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *sharingTool) Execute(_ context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	return ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "shared report.pdf with the user"}},
		Shared:  t.files,
	}, nil
}

func shareCall(id string) provider.ToolCallBlock {
	return provider.ToolCallBlock{ID: id, Name: "share_file", Arguments: json.RawMessage(`{}`)}
}

// The loop stamps the call id, keeps the record OUT of the model's copy, and
// puts it on the tool-role message's Meta so it persists with the turn.
func TestExecuteToolsRecordsSharesOnTheMessageNotTheModelsCopy(t *testing.T) {
	tool := &sharingTool{files: []SharedFile{{ID: "shr_a", Name: "report.pdf", Kind: "document", Size: 12}}}
	a := NewAgent(&advanceFakeClient{}, "fake-model", "system", Registry{"share_file": tool})
	assistant := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{shareCall("call_1")}}

	msg, hadErr := a.executeTools(context.Background(), assistant, a.Tools, func(AgentEvent) {})
	if hadErr {
		t.Fatalf("executeTools reported an error")
	}

	var got []SharedFile
	if err := json.Unmarshal([]byte(msg.Meta[MetaShared]), &got); err != nil {
		t.Fatalf("Meta[%s] = %q: %v", MetaShared, msg.Meta[MetaShared], err)
	}
	if len(got) != 1 || got[0].ID != "shr_a" {
		t.Fatalf("recorded shares = %+v, want the published one", got)
	}
	// The tool did not set CallID and could not have: the loop owns it.
	if got[0].CallID != "call_1" {
		t.Errorf("CallID = %q, want call_1 stamped by the loop", got[0].CallID)
	}

	// The model's copy is the text line and nothing retrievable — no id, no
	// path, nothing it could put in a later request.
	raw, err := json.Marshal(ContentToWire(msg.Content))
	if err != nil {
		t.Fatal(err)
	}
	if body := string(raw); strings.Contains(body, "shr_a") {
		t.Errorf("the tool_result blocks carry the share id: %s", body)
	}
}

// One tool-role message can hold several calls' results, so each card has to say
// which row it belongs to or the client cannot place any of them.
func TestExecuteToolsStampsEachShareWithItsOwnCall(t *testing.T) {
	tool := &sharingTool{files: []SharedFile{{ID: "shr_x", Name: "a.png", Kind: "image"}}}
	a := NewAgent(&advanceFakeClient{}, "fake-model", "system", Registry{"share_file": tool})
	assistant := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		shareCall("call_1"), shareCall("call_2"),
	}}

	msg, _ := a.executeTools(context.Background(), assistant, a.Tools, func(AgentEvent) {})

	var got []SharedFile
	if err := json.Unmarshal([]byte(msg.Meta[MetaShared]), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("recorded %d shares, want 2", len(got))
	}
	if got[0].CallID != "call_1" || got[1].CallID != "call_2" {
		t.Errorf("call ids = %q, %q; want call_1, call_2", got[0].CallID, got[1].CallID)
	}
}

// A turn that shared nothing must not grow a Meta bag — every key in there is
// something a client may eventually key off.
func TestExecuteToolsLeavesMetaAloneWhenNothingWasShared(t *testing.T) {
	tool := &sharingTool{}
	a := NewAgent(&advanceFakeClient{}, "fake-model", "system", Registry{"share_file": tool})
	assistant := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{shareCall("call_1")}}

	msg, _ := a.executeTools(context.Background(), assistant, a.Tools, func(AgentEvent) {})
	if msg.Meta != nil {
		t.Errorf("Meta = %v on a turn that shared nothing, want nil", msg.Meta)
	}
}

// The live path: a remote panel renders from the event, not the message, so the
// record has to survive EventToWire — which is exactly where ToolResult.Details
// stops, and why Shared is a field of its own.
func TestToolResultEventCarriesShares(t *testing.T) {
	ev := EvToolResult{ID: "call_1", Result: ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "shared"}},
		Shared: []SharedFile{{
			ID: "shr_a", CallID: "call_1", Name: "report.pdf",
			Kind: "document", Mime: "application/pdf", Size: 2048,
		}},
	}}

	raw, err := json.Marshal(EventToWire(ev))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"type":"tool_result","id":"call_1","content":[{"type":"text","text":"shared"}],` +
		`"shared":[{"id":"shr_a","call_id":"call_1","name":"report.pdf","kind":"document","mime":"application/pdf","size":2048}]}`
	if string(raw) != want {
		t.Errorf("tool_result wire =\n  %s\nwant\n  %s", raw, want)
	}
}

// The replay path: a message reloaded from disk must produce the same cards the
// live event did, or a share vanishes the moment you refresh the panel.
func TestMessageToWireLiftsSharesOutOfMeta(t *testing.T) {
	msg := provider.Message{
		Role:    provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{CallID: "call_1"}},
		Meta:    map[string]string{MetaShared: `[{"id":"shr_a","call_id":"call_1","name":"a.mp3","kind":"audio"}]`},
	}

	w := MessageToWire(msg)
	if len(w.Shared) != 1 {
		t.Fatalf("Shared = %+v, want one entry", w.Shared)
	}
	if w.Shared[0].ID != "shr_a" || w.Shared[0].CallID != "call_1" || w.Shared[0].Kind != "audio" {
		t.Errorf("Shared[0] = %+v, want the recorded share", w.Shared[0])
	}
}

// The other half of the replay path. MessageToWire lifting the record out of
// Meta is only useful if MessageFromWire puts it back: a control-plane client
// (the TUI) rebuilds its whole transcript through this pair on every snapshot,
// so a share that survives the trip out and dies on the trip back is a card
// that vanishes the moment the session is resumed or the carrier reconnects.
func TestMessageFromWireRestoresShares(t *testing.T) {
	msg := provider.Message{
		Role:    provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{CallID: "call_1"}},
		Meta:    map[string]string{MetaShared: `[{"id":"shr_a","call_id":"call_1","name":"a.mp3","kind":"audio"}]`},
	}

	back := MessageFromWire(MessageToWire(msg))

	var got []SharedFile
	if raw := back.Meta[MetaShared]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("Meta[%s] = %q: %v", MetaShared, raw, err)
		}
	}
	if len(got) != 1 {
		t.Fatalf("shares after the round trip = %+v, want the one that was recorded", got)
	}
	if got[0].ID != "shr_a" || got[0].CallID != "call_1" || got[0].Kind != "audio" {
		t.Errorf("restored share = %+v, want the recorded one intact", got[0])
	}
}

// A message that shared nothing must not grow a Meta bag on the way back, for
// the reason the execute path must not: every key in there is something a
// client may key off, and an empty one is a lie about what the turn did.
func TestMessageFromWireLeavesMetaAloneWithoutShares(t *testing.T) {
	back := MessageFromWire(MessageToWire(provider.Message{
		Role:    provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{CallID: "call_1"}},
	}))
	if back.Meta != nil {
		t.Errorf("Meta = %v on a message that shared nothing, want nil", back.Meta)
	}
}

// A record that will not parse costs the user a download card. Costing them the
// whole transcript instead would be the worse trade, so the message still
// renders — the same tolerance the attachments field gets.
func TestMessageToWireSurvivesAMalformedShareRecord(t *testing.T) {
	msg := provider.Message{
		Role:    provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{CallID: "call_1"}},
		Meta:    map[string]string{MetaShared: `{"not":"an array"`},
	}

	w := MessageToWire(msg)
	if len(w.Shared) != 0 {
		t.Errorf("Shared = %+v, want nothing from a malformed record", w.Shared)
	}
	if len(w.Content) != 1 {
		t.Errorf("Content = %+v, want the tool result to render regardless", w.Content)
	}
}

// The point of hanging the record on the message: reopen the session days later
// and the cards are still there, in the right places, with nothing to join.
func TestSharesSurviveASessionReload(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "anthropic", "claude", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	shared := []SharedFile{{ID: "shr_a", CallID: "call_1", Name: "report.pdf", Kind: "document", Size: 9}}
	raw, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{
		Role:    provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{CallID: "call_1"}},
		Meta:    map[string]string{MetaShared: string(raw)},
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

	var found []SharedFile
	for _, m := range msgs {
		found = append(found, MessageToWire(m).Shared...)
	}
	if len(found) != 1 || found[0].ID != "shr_a" || found[0].CallID != "call_1" {
		t.Fatalf("shares after reload = %+v, want the one that was recorded", found)
	}
}
