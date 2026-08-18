package workspace

import (
	"context"
	"fmt"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seedLiveSession builds a live workspace session holding n alternating
// user/assistant messages ("m0".."m<n-1>"), on a fresh TERVA_HOME.
func seedLiveSession(t *testing.T, n int) (*Workspace, *wsSession) {
	t.Helper()
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}

	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("created session is not live")
	}
	msgs := make([]provider.Message, 0, n)
	for i := 0; i < n; i++ {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		m := provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("m%d", i)}}}
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	s.agent.SetMessages(msgs)
	return w, s
}

func markIndices(s *wsSession) []int {
	var out []int
	for _, m := range s.snapshot().VariantMarks {
		if !m.Span {
			out = append(out, m.Index)
		}
	}
	return out
}

// A message-scoped variant mark is keyed by transcript index. Deleting a message
// BELOW it shifts every later message down one — so the mark must move too, or it
// names a different message than the one it describes.
//
// The file-replay half did this from the start (core.ShiftVariantKeysOnDelete,
// called by walkSession, pinned by TestMessageVariantShiftsOnDelete). The LIVE
// half did not: deleteMessage removed the message and invalidated the tail span
// but never touched s.msgVars.
//
// The consequence was not cosmetic. With [m0,m1,m2,m3], editing index 3 and then
// deleting index 0 left the daemon advertising a mark at index 3 — which, after
// the shift, is out of range or names the wrong message — while the file's
// variant had correctly moved to index 2. Live and file disagreed permanently,
// and a swipe at the advertised index acted on a message the user never varied.
func TestDeleteRealignsMessageVariantMarks(t *testing.T) {
	_, s := seedLiveSession(t, 4)

	// Vary the last message, creating a message-scoped mark at index 3.
	edited := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "m3-edited"}}}
	if err := s.editMessageAsVariant(s.agent.Messages(), 3, edited); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := markIndices(s); len(got) != 1 || got[0] != 3 {
		t.Fatalf("marks after edit = %v, want [3]", got)
	}

	// Delete a message BELOW the mark.
	if err := s.deleteMessage(s.agent.TranscriptEpoch(), 0); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got := markIndices(s)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("marks after deleting index 0 = %v, want [2].\n"+
			"The live variant marks did not follow the shifted transcript, so the daemon is "+
			"advertising a swipe position that names a different message than the one it describes. "+
			"The file-replay half shifts correctly, so live and file now disagree.", got)
	}

	// And the mark must describe the message it now points at.
	msgs := s.agent.Messages()
	if len(msgs) != 3 {
		t.Fatalf("transcript length = %d, want 3", len(msgs))
	}
	if txt := textOfMsg(msgs[2]); txt != "m3-edited" {
		t.Errorf("message at the marked index is %q, want the edited one (%q)", txt, "m3-edited")
	}
}

// A clear replaces the whole transcript. Marks naming the old one must go, or the
// daemon advertises a swipe position on an EMPTY transcript.
func TestClearForgetsMessageVariantMarks(t *testing.T) {
	_, s := seedLiveSession(t, 4)
	edited := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "m3-edited"}}}
	if err := s.editMessageAsVariant(s.agent.Messages(), 3, edited); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := markIndices(s); len(got) != 1 {
		t.Fatalf("marks after edit = %v, want one", got)
	}

	if err := s.clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := markIndices(s); len(got) != 0 {
		t.Fatalf("marks after clear = %v, want none — the transcript they name no longer exists", got)
	}
}

func textOfMsg(m provider.Message) string {
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			return tb.Text
		}
	}
	return ""
}
