package discord

import (
	"context"
	"os"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/connsdk"
)

// TestLiveSmoke exercises the real Discord API — gateway handshake and
// identity — against a scratch bot. Skipped unless
// TERVA_DISCORD_TEST_TOKEN is set; never runs in CI. Message and
// button round trips need a channel id too:
// TERVA_DISCORD_TEST_CHANNEL sends one message there.
func TestLiveSmoke(t *testing.T) {
	token := os.Getenv("TERVA_DISCORD_TEST_TOKEN")
	if token == "" {
		t.Skip("set TERVA_DISCORD_TEST_TOKEN to run the live smoke")
	}
	tr, err := NewTransport(token, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := tr.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Logf("connected as @%s (id=%s)", id.Username, id.ID)

	if ch := os.Getenv("TERVA_DISCORD_TEST_CHANNEL"); ch != "" {
		rctx, rcancel := context.WithCancel(ctx)
		go func() { _ = tr.Receive(rctx, func(connsdk.Message) {}) }()
		defer rcancel()
		time.Sleep(2 * time.Second) // gateway settle
		if err := tr.Typing(ctx, ch); err != nil {
			t.Errorf("Typing: %v", err)
		}
		if err := tr.Send(ctx, connsdk.Outgoing{ChatID: ch, Text: "terva discord connector live smoke ✓"}); err != nil {
			t.Errorf("Send: %v", err)
		}
	}
}
