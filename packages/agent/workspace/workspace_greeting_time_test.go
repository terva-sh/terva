package workspace

import (
	"context"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The deferred greeting persisted with a ZERO Time — "0001-01-01T00:00:00Z" as
// the opening beat of every Stage chat. Invisible in Stage itself, which draws no
// per-message timestamps, which is exactly why it survived: the first row of the
// file dated to the year 1, and nothing on screen ever said so. Anything reading
// the session as a timeline — the session player, a replay, a time-sorted export
// — starts two millennia before the scene.
//
// The CLI's own greeting seed (seedCardGreeting) always stamped one; only the
// deferred path built the message without it.
func TestDeferredGreetingIsTimestamped(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	before := time.Now().Add(-time.Minute)

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hi there."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live == nil {
		t.Fatal("session is not live")
	}

	// Live, before it ever reaches disk.
	msgs := live.agent.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected the seeded greeting, got %d messages", len(msgs))
	}
	if msgs[0].Time.IsZero() {
		t.Error("the live greeting carries a zero Time")
	}

	// And through the flush, which is what the session file actually keeps.
	if err := live.sess.AppendMessage(provider.Message{
		Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	_, replayed, err := core.OpenSession(live.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) == 0 {
		t.Fatal("nothing replayed")
	}
	greeting := replayed[0]
	if greeting.Meta[core.MetaSource] != "card:greeting" {
		t.Fatalf("first replayed row is not the greeting: meta=%v", greeting.Meta)
	}
	if greeting.Time.IsZero() {
		t.Fatal("the PERSISTED greeting still has a zero Time — the session file opens in year 1")
	}
	if greeting.Time.Before(before) || greeting.Time.After(time.Now().Add(time.Minute)) {
		t.Errorf("greeting Time = %s, not a plausible open time", greeting.Time)
	}
	// It must not sort after the user's first line, or a time-ordered replay puts
	// the character's opening beat second.
	if len(replayed) > 1 && !replayed[1].Time.IsZero() && greeting.Time.After(replayed[1].Time) {
		t.Errorf("greeting (%s) is later than the reply to it (%s)", greeting.Time, replayed[1].Time)
	}
}
