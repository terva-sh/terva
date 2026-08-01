package chat

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// saturatingClient ends the turn with the gauge past the auto-compact
// threshold, then refuses the compaction the way an overloaded provider does.
type saturatingClient struct{ calls atomic.Int32 }

func (c *saturatingClient) Name() string { return "saturating" }

func (c *saturatingClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := c.calls.Add(1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		if call == 1 {
			// 95% of claude-sonnet-4-5's 200k window.
			out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 190_000}}
			out <- provider.EventTextDelta{Delta: "ok"}
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "ok"}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewAPIError(
			"openai-codex", "Our servers are currently overloaded. Please try again later.", true)}
	}()
	return out, nil
}

// The paired user is told when a compaction starts. Discarding the error meant
// the one that ran on their behalf after the turn could fail with nothing said
// at all — leaving their next message to pay a latency, or hit a limit, that
// had already been diagnosed and thrown away.
func TestChatPostTurnAutoCompactFailureIsReported(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	client := &saturatingClient{}
	agent := core.NewAgent(client, "claude-sonnet-4-5", "sys", core.Registry{})
	agent.MaxRetries = 0 // the ladder is proven in core; this is about the report

	// A transcript comfortably past keep-tail, so CanCompact is true.
	seed := make([]provider.Message, 0, 8)
	for range 4 {
		seed = append(seed,
			provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "q"}}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a"}}},
		)
	}
	agent.SetMessages(seed)

	l := &Loop{
		Connector: conn,
		Agent:     agent,
		Provider:  "fake",
		CWD:       "/ws",
		Pairing:   pairedWith("7"),
		Info:      func(string) {},
		Warn:      func(string) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Run(ctx) }()

	conn.inbound <- msgFrom("7", "go")
	// The reply, then the compaction's outcome.
	sends := conn.waitSends(t, 2)

	var reported bool
	for _, out := range sends {
		if strings.Contains(out.Text, "could not condense") && strings.Contains(out.Text, "overloaded") {
			reported = true
		}
	}
	if !reported {
		var got []string
		for _, out := range sends {
			got = append(got, out.Text)
		}
		t.Fatalf("the failed post-turn compaction was never reported to the paired chat; sends = %q", got)
	}

	if got := client.calls.Load(); got != 2 {
		t.Errorf("Stream calls = %d; want 2 (turn + the compaction that failed)", got)
	}
}
