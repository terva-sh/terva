package extconn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// scriptedClient is a provider.Client whose every turn replies with
// fixed text (the chat loop tests' fake, minimally reimplemented).
type scriptedClient struct{ reply string }

func (c *scriptedClient) Name() string { return "scripted" }

func (c *scriptedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventTextDelta{Delta: c.reply}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.reply}},
		}}
	}()
	return out, nil
}

// TestLoopEndToEnd is the whole point of the experiment in one test: a
// single subprocess (shell stand-in for a dual-role plugin) delivers a
// chat message through the tunnel, the REAL chat.Loop gates it and runs
// a REAL agent turn (scripted provider), and the reply lands back in
// that same subprocess as an inner send frame. Extension → turn →
// extension, one process on the far side of the wire.
func TestLoopEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	sends := filepath.Join(tmp, "sends.log")

	body := `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"e2e-loop","protocol_min":1,"protocol_max":1,"capabilities":{}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"bot","username":"bot"}}\n' "$sid"
      printf '{"type":"chat","id":"%s","frame":{"type":"message","chat_id":"c1","user_id":"u1","username":"drew","text":"hello agent"}}\n' "$sid"
      ;;
    *'"frame":{"type":"send"'*)
      rid=$(printf '%s' "$line" | sed -n 's/.*"frame":{"type":"send","id":"\([^"]*\)".*/\1/p')
      printf '%s\n' "$line" >> ` + sends + `
      printf '{"type":"chat","id":"%s","frame":{"type":"result","id":"%s"}}\n' "$sid" "$rid"
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	bindDriver(t, tmp, "e2e-loop", body)

	conn := NewConn("e2e-loop", func(string) {})
	loop := &chat.Loop{
		Connector: conn,
		Agent:     core.NewAgent(&scriptedClient{reply: "scripted-reply"}, "fake-model", "sys", core.Registry{}),
		Provider:  "fake",
		CWD:       "/ws",
		Pairing:   chat.Pairing{AllowedUserID: "u1"}, // pre-paired to the script's sender
		Info:      func(string) {},
		Warn:      func(string) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = loop.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(sends)
		if err == nil && strings.Contains(string(b), "scripted-reply") {
			return // the agent's reply reached the extension process
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(sends)
	t.Fatalf("the agent's reply never reached the extension; sends.log = %q", string(b))
}
