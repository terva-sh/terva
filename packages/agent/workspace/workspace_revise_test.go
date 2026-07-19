package workspace

import (
	"context"
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func reviseText(m provider.Message) string {
	if len(m.Content) == 0 {
		return ""
	}
	tb, _ := m.Content[0].(provider.TextBlock)
	return tb.Text
}

func reviseTexts(ms []provider.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = reviseText(m)
	}
	return out
}

// TestEditDeleteMessageReviseTranscript drives the Phase-1c wire verbs through
// the real workspace: an edit and a delete revise the live transcript, persist as
// amend rows (a reload from disk matches the live transcript), and a stale epoch
// is refused with CodeConflict rather than applied to the wrong message.
func TestEditDeleteMessageReviseTranscript(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("created session is not live")
	}

	// Seed a transcript into both the file and the agent, as a real turn would.
	base := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a0"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u1"}}},
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)
	epoch := s.agent.TranscriptEpoch()

	// Edit the assistant message in place.
	if err := w.EditMessage(context.Background(), info.ID, epoch, 1, "a0-edited"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	// The edit advanced the epoch, so the original one is now stale → CodeConflict.
	if err := w.EditMessage(context.Background(), info.ID, epoch, 0, "stale"); err == nil {
		t.Error("edit with a stale epoch should be refused (CodeConflict)")
	}
	// Delete the trailing user message at the current epoch.
	epoch2 := s.agent.TranscriptEpoch()
	if err := w.DeleteMessage(context.Background(), info.ID, epoch2, 2); err != nil {
		t.Fatalf("delete: %v", err)
	}

	want := []string{"u0", "a0-edited"}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, want) {
		t.Errorf("live transcript = %v, want %v", got, want)
	}
	// Persisted: a reload from disk reproduces the revised transcript.
	_, reloaded, err := core.OpenSession(info.Path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reviseTexts(reloaded); !reflect.DeepEqual(got, want) {
		t.Errorf("reloaded transcript = %v, want %v", got, want)
	}

	// Out-of-range index is a clean bad-request, not a panic.
	if err := w.DeleteMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 99); err == nil {
		t.Error("delete out of range should error")
	}
}
