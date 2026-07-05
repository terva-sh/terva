package replay

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "hi there"}}},
	}
	for _, m := range msgs {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.AppendUsage(provider.Usage{InputTokens: 12}, provider.Usage{InputTokens: 12, CostUSD: 0.01}); err != nil {
		t.Fatal(err)
	}
	return path
}

// drainSnapshots collects every event currently buffered on ch, returning the
// conversation event types and the most recent snapshot seen.
func drainSnapshots(ch <-chan ctrlproto.Event) (types []string, lastSnap *ctrlproto.Snapshot) {
	for {
		select {
		case ev := <-ch:
			if ev.Type == ctrlproto.EventSnapshot {
				lastSnap = ev.Snapshot
				continue
			}
			types = append(types, ev.Type)
		default:
			return types, lastSnap
		}
	}
}

func TestCarrierSubscribeSnapshotThenStream(t *testing.T) {
	c, err := Open(writeFixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ch, err := c.Subscribe(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}

	first := <-ch
	if first.Type != ctrlproto.EventSnapshot {
		t.Fatalf("first event = %q, want snapshot", first.Type)
	}
	if first.Snapshot == nil || len(first.Snapshot.Messages) != 0 {
		t.Fatalf("initial snapshot should be empty (playhead at 0), got %+v", first.Snapshot)
	}
	if first.Snapshot.Session.Model != "model" || first.Snapshot.Session.Provider != "prov" {
		t.Errorf("session descriptor = %+v", first.Snapshot.Session)
	}

	// Step the whole scene (events buffer in the channel), then read them.
	for c.Step() {
	}
	got, _ := drainSnapshots(ch)
	for _, want := range []string{"user_message", "turn_start", "assistant_start", "text_delta", "turn_end", "assistant_message", "done"} {
		if !slices.Contains(got, want) {
			t.Errorf("stream missing %q; got %v", want, got)
		}
	}
}

func TestCarrierSeekResyncs(t *testing.T) {
	c, err := Open(writeFixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ch, _ := c.Subscribe(t.Context(), "")
	drainSnapshots(ch) // consume initial snapshot

	total := c.Transport().Total
	c.Seek(total)
	if _, snap := drainSnapshots(ch); snap == nil || len(snap.Messages) != 2 {
		t.Fatalf("seek to end: snapshot should have 2 messages, got %+v", snap)
	}
	c.Seek(0)
	if _, snap := drainSnapshots(ch); snap == nil || len(snap.Messages) != 0 {
		t.Fatalf("seek to 0: snapshot should be empty, got %+v", snap)
	}
}

// TestCarrierEffectiveCompactionCollapses proves the effective/raw distinction:
// effective honors the checkpoint (the transcript collapses to the summary),
// raw plays the full uncompacted history.
func TestCarrierEffectiveCompactionCollapses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	um := func(s string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: s}}}
	}
	am := func(s string) provider.Message {
		return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: s}}}
	}
	for _, m := range []provider.Message{um("one"), am("first"), um("two"), am("second")} {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.AppendCompaction([]provider.Message{am("[summary]")}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{um("three"), am("third")} {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	endTranscript := func(mode Mode) int {
		c, err := Open(path, Options{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ch, _ := c.Subscribe(t.Context(), "")
		drainSnapshots(ch)
		c.Seek(c.Transport().Total)
		_, snap := drainSnapshots(ch)
		if snap == nil {
			t.Fatalf("%s: no snapshot", mode)
		}
		return len(snap.Messages)
	}

	// Effective collapses to summary + the post-compaction turn (3);
	// raw keeps all six messages.
	if got := endTranscript(ModeEffective); got != 3 {
		t.Errorf("effective end transcript = %d messages, want 3 (summary+three+third)", got)
	}
	if got := endTranscript(ModeRaw); got != 6 {
		t.Errorf("raw end transcript = %d messages, want 6 (uncollapsed)", got)
	}
}

func TestCarrierAutoplayOnSubscribe(t *testing.T) {
	fast := Pace{TextRunes: 100, TextInterval: time.Millisecond, Think: time.Millisecond, Tool: time.Millisecond, Compact: time.Millisecond}
	c, err := Open(writeFixture(t), Options{Autoplay: true, Pace: fast})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ch, _ := c.Subscribe(t.Context(), "")
	// With no manual Play(), autoplay drives the scene to its terminal "done".
	deadline := time.After(2 * time.Second)
	gotUser, gotDone := false, false
	for !gotDone {
		select {
		case <-deadline:
			t.Fatalf("autoplay did not reach done; user=%v", gotUser)
		case ev := <-ch:
			switch ev.Type {
			case "user_message":
				gotUser = true
			case "done":
				gotDone = true
			}
		}
	}
	if !gotUser {
		t.Error("expected a user_message event during autoplay")
	}
}

func TestCarrierSeekByTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	um := func(s string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: s}}}
	}
	am := func(s string) provider.Message {
		return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: s}}}
	}
	for _, m := range []provider.Message{um("q1"), am("a1"), um("q2"), am("a2"), um("q3"), am("a3")} {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := t.Context()
	turn := func(delta int) int {
		st, _ := c.ReplayControl(ctx, "", ctrlproto.ReplayControlParams{Action: "turn", Position: delta})
		return st.Position
	}

	if len(c.turnStarts) != 3 || c.turnStarts[0] != 0 {
		t.Fatalf("turnStarts = %v, want 3 with first at frame 0", c.turnStarts)
	}
	if got := turn(1); got != c.turnStarts[1] {
		t.Errorf("next turn from start = %d, want %d", got, c.turnStarts[1])
	}
	if got := turn(1); got != c.turnStarts[2] {
		t.Errorf("next turn = %d, want %d", got, c.turnStarts[2])
	}
	if got := turn(-1); got != c.turnStarts[1] {
		t.Errorf("prev turn = %d, want %d", got, c.turnStarts[1])
	}
	// Next past the last turn lands at the end.
	c.Seek(c.turnStarts[2])
	if got, total := turn(1), c.Transport().Total; got != total {
		t.Errorf("next past the last turn = %d, want end %d", got, total)
	}
}

func TestCarrierRejectsMutations(t *testing.T) {
	c, err := Open(writeFixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	unsup := func(err error) bool {
		var ce *ctrlproto.Error
		return errors.As(err, &ce) && ce.Code == ctrlproto.CodeUnsupported
	}
	ctx := context.Background()
	if err := c.Prompt(ctx, "", "hi", nil); !unsup(err) {
		t.Errorf("Prompt: want CodeUnsupported, got %v", err)
	}
	if err := c.Compact(ctx, ""); !unsup(err) {
		t.Errorf("Compact: want CodeUnsupported, got %v", err)
	}
	if _, err := c.CreateSession(ctx, ctrlproto.CreateOpts{}); !unsup(err) {
		t.Errorf("CreateSession: want CodeUnsupported, got %v", err)
	}
	// Cancel/Approve/Answer are benign no-ops.
	if err := c.Cancel(ctx, ""); err != nil {
		t.Errorf("Cancel should be a no-op, got %v", err)
	}
}
