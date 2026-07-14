package workspace

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// historyWorkspace is a workspace holding one session whose transcript is n numbered
// user messages ("m0"… "m<n-1>"), so a page can be checked by name rather than by
// counting.
func historyWorkspace(t *testing.T, n int) (*Workspace, *wsSession) {
	t.Helper()
	s := &wsSession{
		id:    "s1",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:   newWSHub(),
		agent: core.NewAgent(nil, "claude-sonnet-4-5", "", core.Registry{}),
	}
	msgs := make([]provider.Message, 0, n)
	for i := range n {
		msgs = append(msgs, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "m" + strconv.Itoa(i)}},
		})
	}
	s.agent.SetMessages(msgs)

	w := s.ws
	w.sessions = map[string]*wsSession{"s1": s}
	return w, s
}

func historyTexts(res ctrlproto.HistoryResult) []string {
	out := make([]string, 0, len(res.Messages))
	for _, m := range res.Messages {
		if len(m.Content) > 0 {
			out = append(out, m.Content[0].Text)
		}
	}
	return out
}

// TestHistoryPagesBackwardFromTheWindow walks up a transcript exactly as a client
// with a windowed snapshot does: ask for what is above what you hold, prepend, repeat
// until Base is 0.
func TestHistoryPagesBackwardFromTheWindow(t *testing.T) {
	w, s := historyWorkspace(t, 25)
	epoch := s.agent.TranscriptEpoch()

	// The client holds the tail [15, 25) and wants what is above it.
	res, err := w.History(context.Background(), "s1", 15, 10, epoch)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if res.Base != 5 || res.Total != 25 {
		t.Errorf("base=%d total=%d, want base=5 total=25", res.Base, res.Total)
	}
	if got := historyTexts(res); len(got) != 10 || got[0] != "m5" || got[9] != "m14" {
		t.Fatalf("page = %v, want m5..m14", got)
	}

	// One more page reaches the start. Base 0 is how a client knows it has arrived —
	// beyond it lies whatever a compaction folded away, which is conversation.reveal's
	// job, not this one's.
	res, err = w.History(context.Background(), "s1", res.Base, 10, epoch)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if res.Base != 0 {
		t.Errorf("base = %d, want 0 — the top of the live transcript", res.Base)
	}
	if got := historyTexts(res); len(got) != 5 || got[0] != "m0" || got[4] != "m4" {
		t.Fatalf("page = %v, want m0..m4 (a short final page, not an error)", got)
	}

	// Asking above the top is a perfectly sensible thing for a client at Base 0 to do.
	// It gets nothing, not an error.
	res, err = w.History(context.Background(), "s1", 0, 10, epoch)
	if err != nil {
		t.Fatalf("history at the top: %v", err)
	}
	if len(res.Messages) != 0 || res.Base != 0 {
		t.Errorf("asking above the top returned %d messages at base %d", len(res.Messages), res.Base)
	}
}

// TestHistoryRefusesAStaleEpoch is the reason the epoch is on the wire at all.
//
// An index only names a message within the transcript it was taken from. Compaction
// REPLACES that transcript — so "give me messages 5..15" from a client that has not
// noticed would return whatever now happens to sit at those indexes: a different
// conversation, silently, in the middle of someone's scrollback. Say no instead.
func TestHistoryRefusesAStaleEpoch(t *testing.T) {
	w, s := historyWorkspace(t, 25)
	stale := s.agent.TranscriptEpoch()

	// What a compaction does to the transcript, without needing a model to do it.
	s.agent.SetMessages([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "## Context Summary (compacted)"}},
	}})
	if s.agent.TranscriptEpoch() == stale {
		t.Fatal("SetMessages did not bump the transcript epoch — the whole scheme rests on this")
	}

	_, err := w.History(context.Background(), "s1", 15, 10, stale)
	if err == nil {
		t.Fatal("history served a page indexed into a transcript that no longer exists")
	}
	var cerr *ctrlproto.Error
	if !errors.As(err, &cerr) || cerr.Code != ctrlproto.CodeConflict {
		t.Fatalf("err = %v, want a %q — the caller was right and then the world changed", err, ctrlproto.CodeConflict)
	}
	if !strings.Contains(err.Error(), "compacted") {
		t.Errorf("the error should say what happened, not just that it failed: %v", err)
	}

	// The current epoch still works, on the new transcript.
	res, err := w.History(context.Background(), "s1", 1, 10, s.agent.TranscriptEpoch())
	if err != nil {
		t.Fatalf("history at the live epoch: %v", err)
	}
	if got := historyTexts(res); len(got) != 1 || !strings.Contains(got[0], "Context Summary") {
		t.Errorf("page = %v, want the post-compaction transcript", got)
	}
}

// Epoch 0 means "I am not tracking it" — an older client, or one that never asked for
// a window. It must still be served, or adding the field breaks them.
func TestHistorySkipsTheCheckWhenTheClientDoesNotTrackTheEpoch(t *testing.T) {
	w, _ := historyWorkspace(t, 25)
	res, err := w.History(context.Background(), "s1", 25, 5, 0)
	if err != nil {
		t.Fatalf("history without an epoch: %v", err)
	}
	if got := historyTexts(res); len(got) != 5 || got[4] != "m24" {
		t.Fatalf("page = %v, want the last five", got)
	}
}
