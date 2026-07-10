package modes

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/agent/replay"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// TestCarrierReplayStateStashed verifies the pump records a replay_state event
// (for the transport keys and the scrubber).
func TestCarrierReplayStateStashed(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.handleCarrierEvent(ctrlproto.ReplayStateEvent(ctrlproto.ReplayState{
		Playing: true, Position: 5, Total: 40, Speed: 2,
	}))
	i.mu.Lock()
	got := i.replayState
	i.mu.Unlock()
	if !got.Playing || got.Position != 5 || got.Total != 40 || got.Speed != 2 {
		t.Fatalf("replayState = %+v", got)
	}
}

func writeReplayFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "hello there"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "again"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "second reply"}}},
	} {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func newReplayTestInteractive(t *testing.T, c *replay.Carrier) *Interactive {
	t.Helper()
	th := tui.Dark
	i := newCtrlprotoTestInteractive()
	i.ed = tui.NewEditor(th.AccentBar(th.Accent))
	i.suggest = newSlashSuggester()
	i.fileSuggest = widgets.NewFileSuggester()
	i.cfg.Carrier = c // sess is ignored by the single-session replay carrier
	return i
}

// TestReplayTransportKeys drives the playback keys against a real carrier.
func TestReplayTransportKeys(t *testing.T) {
	c, err := replay.Open(writeReplayFixture(t), replay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	i := newReplayTestInteractive(t, c)
	ctx := t.Context()

	// Starts paused; space toggles play/pause.
	if !i.handleReplayKey(ctx, tui.Key{Kind: tui.KeyRune, Rune: ' '}) {
		t.Fatal("space should be consumed")
	}
	if !c.Transport().Playing {
		t.Error("space should start playback")
	}
	i.handleReplayKey(ctx, tui.Key{Kind: tui.KeyRune, Rune: ' '})
	if c.Transport().Playing {
		t.Error("second space should pause")
	}

	// Right steps one frame forward from the (now paused) playhead.
	before := c.Transport().Pos
	if !i.handleReplayKey(ctx, tui.Key{Kind: tui.KeyRight}) {
		t.Fatal("Right should be consumed")
	}
	if got := c.Transport().Pos; got != before+1 {
		t.Errorf("Right should step one frame: pos %d -> %d", before, got)
	}

	// Shift+Right jumps a whole turn (to the 2nd turn's frame), distinct from a
	// one-frame step.
	c.Seek(0)
	if !i.handleReplayKey(ctx, tui.Key{Kind: tui.KeyRight, Shift: true}) {
		t.Fatal("shift+Right should be consumed")
	}
	if c.Transport().Pos <= 1 {
		t.Errorf("shift+Right should jump a whole turn, landed at frame %d", c.Transport().Pos)
	}

	// A non-transport key falls through (not consumed by replay).
	if i.handleReplayKey(ctx, tui.Key{Kind: tui.KeyUp}) {
		t.Error("Up should not be claimed by replay transport")
	}
}

// TestReplayKeysInertWithoutCarrier ensures a live (non-replay) session ignores
// the transport handler and the scrubber entirely.
func TestReplayKeysInertWithoutCarrier(t *testing.T) {
	i := newCtrlprotoTestInteractive() // no Carrier
	if i.handleReplayKey(t.Context(), tui.Key{Kind: tui.KeyRune, Rune: ' '}) {
		t.Error("handleReplayKey must be a no-op without a replay carrier")
	}
	if s := i.replayScrubber(); s != "" {
		t.Errorf("replayScrubber must be empty without a replay carrier, got %q", s)
	}
}

func TestReplayScrubber(t *testing.T) {
	c, err := replay.Open(writeReplayFixture(t), replay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	i := newReplayTestInteractive(t, c)

	if s := i.replayScrubber(); s != "" {
		t.Errorf("no state yet should scrub to nothing, got %q", s)
	}
	cases := []struct {
		st   ctrlproto.ReplayState
		want string
	}{
		{ctrlproto.ReplayState{Playing: true, Position: 10, Total: 40, Speed: 1}, "▶ 25%"},
		{ctrlproto.ReplayState{Playing: false, Position: 20, Total: 40, Speed: 2}, "⏸ 50% 2×"},
		{ctrlproto.ReplayState{Position: 40, Total: 40, Speed: 1}, "⏹ 100%"},
	}
	for _, tc := range cases {
		i.mu.Lock()
		i.replayState = tc.st
		i.mu.Unlock()
		if got := i.replayScrubber(); got != tc.want {
			t.Errorf("scrubber(%+v) = %q, want %q", tc.st, got, tc.want)
		}
	}
}
