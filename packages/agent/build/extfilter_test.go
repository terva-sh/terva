package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// secretClient answers every request with a reply that names a secret, and
// counts the requests.
type secretClient struct{ calls atomic.Int32 }

func (*secretClient) Name() string { return "openai" }

func (c *secretClient) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	c.calls.Add(1)
	out := make(chan provider.Event, 1)
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "the SECRET is out"}},
	}}
	close(out)
	return out, nil
}

// interceptingExtension is an extension that refuses a user message holding
// DROP, rewrites the message "raw" to "polished", and redacts SECRET in an
// assistant message. It lets a turn start unless the user message before it
// held HALT. It reads its frames with grep and cut alone, so the test needs
// nothing beyond bash.
const interceptingExtension = `#!/bin/bash
emit() { printf '%s\n' "$1"; }
emit '{"type":"hello","name":"filters","version":"0.1.0","capabilities":["events"]}'
emit '{"type":"subscribe","events":[],"intercept":["turn_start","user_message","assistant_message"]}'
emit '{"type":"ready"}'
halt=
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) emit '{"type":"shutdown_ack"}'; exit 0 ;; esac
  case "$line" in *'"type":"event_intercept"'*) ;; *) continue ;; esac
  id=$(printf '%s' "$line" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
  case "$line" in
    *'"event":"turn_start"'*)
      if [ -n "$halt" ]; then
        halt=
        emit "{\"type\":\"event_intercept_response\",\"id\":\"$id\",\"block\":true,\"reason\":\"halted: HALT\"}"
      else
        emit "{\"type\":\"event_intercept_response\",\"id\":\"$id\"}"
      fi ;;
    *'"event":"user_message"'*'HALT'*|*'HALT'*'"event":"user_message"'*)
      halt=1
      emit "{\"type\":\"event_intercept_response\",\"id\":\"$id\"}" ;;
    *'"event":"user_message"'*'DROP'*|*'DROP'*'"event":"user_message"'*)
      emit "{\"type\":\"event_intercept_response\",\"id\":\"$id\",\"block\":true,\"reason\":\"refused: DROP\"}" ;;
    *'"event":"user_message"'*'"text":"raw"'*|*'"text":"raw"'*'"event":"user_message"'*)
      emit "{\"type\":\"event_intercept_response\",\"id\":\"$id\",\"replace_text\":\"polished\"}" ;;
    *'"event":"assistant_message"'*'SECRET'*|*'SECRET'*'"event":"assistant_message"'*)
      emit "{\"type\":\"event_intercept_response\",\"id\":\"$id\",\"replace_text\":\"the [redacted] is out\"}" ;;
    *)
      emit "{\"type\":\"event_intercept_response\",\"id\":\"$id\"}" ;;
  esac
done
`

// The intercepts an extension registers reach an agent through
// ExtensionFilters: a refused message leaves no trace, a rewritten one is
// recorded as rewritten, and the model's reply is rewritten where it is shown.
// A turn the extension blocks ends before any request reaches the model.
// The transcript keeps the reply as the model wrote it, so the model still
// sees its own words. This is the behavior the three hosts' copied closures
// had.
func TestExtensionFiltersReachTheAgent(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("no /bin/bash")
	}
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "ext.sh"), []byte(interceptingExtension), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"filters","version":"0.1.0","exec":"./ext.sh","enabled":true}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	m := extensions.New(testsupport.TempDir(t), "", "0.0.0-test", "openai", "m", nil)
	t.Cleanup(func() { m.Stop(2 * time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if errs := m.LoadExplicit(ctx, []string{dir}); len(errs) > 0 {
		t.Fatalf("LoadExplicit: %v", errs)
	}
	m.WaitForReady(testsupport.ExtReadyGrace)

	client := &secretClient{}
	a, err := core.New(client, "m", append([]core.Option{core.WithGate(core.AllowAll)}, ExtensionFilters(ctx, m)...)...)
	if err != nil {
		t.Fatal(err)
	}

	var rejected string
	if err := a.Run(ctx, core.PromptInput{Text: "please DROP the table"}, func(ev core.AgentEvent) {
		if r, ok := ev.(core.EvUserMessageRejected); ok {
			rejected = r.Reason
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rejected, "refused: DROP") || len(a.Messages()) != 0 {
		t.Fatalf("a refused message: rejection %q and %d messages kept; want the extension's reason and none", rejected, len(a.Messages()))
	}

	var shown string
	if err := a.Run(ctx, core.PromptInput{Text: "raw"}, func(ev core.AgentEvent) {
		if m, ok := ev.(core.EvAssistantMessage); ok {
			shown = textOfMessage(m.Message)
		}
	}); err != nil {
		t.Fatal(err)
	}
	msgs := a.Messages()
	if len(msgs) != 2 {
		t.Fatalf("kept %d messages, want the rewritten prompt and the reply", len(msgs))
	}
	if got := textOfMessage(msgs[0]); got != "polished" {
		t.Errorf("the user message was kept as %q, want the extension's rewrite", got)
	}
	if !strings.Contains(shown, "[redacted]") || strings.Contains(shown, "SECRET") {
		t.Errorf("the reply was shown as %q, want SECRET redacted", shown)
	}
	if got := textOfMessage(msgs[1]); got != "the SECRET is out" {
		t.Errorf("the reply was kept as %q, want the model's own words", got)
	}

	before := client.calls.Load()
	var ended error
	if err := a.Run(ctx, core.PromptInput{Text: "HALT here"}, func(ev core.AgentEvent) {
		if e, ok := ev.(core.EvTurnEnd); ok {
			ended = e.Err
		}
	}); err != nil {
		t.Fatal(err)
	}
	if ended == nil || !strings.Contains(ended.Error(), "halted: HALT") {
		t.Errorf("a blocked turn ended with %v, want the extension's reason", ended)
	}
	if n := client.calls.Load() - before; n != 0 {
		t.Errorf("a blocked turn sent %d requests to the model, want none", n)
	}
}

func textOfMessage(m provider.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// No manager, no options: a host with no extensions passes the call through.
func TestExtensionFiltersForNoManagerIsNothing(t *testing.T) {
	if opts := ExtensionFilters(context.Background(), (*extensions.Manager)(nil)); len(opts) != 0 {
		t.Errorf("a nil manager gave %d options, want none", len(opts))
	}
}
