package sdk

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// replyClient answers every request, a compaction's summary request included,
// with a short streamed reply.
type replyClient struct{}

func (replyClient) Name() string { return "reply" }

func (replyClient) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	out <- provider.EventTextDelta{Delta: "ok"}
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}}
	close(out)
	return out, nil
}

type afterTurnOnly struct{}

func (afterTurnOnly) Decide(s core.CompactionState) core.CompactionDecision {
	return core.CompactionDecision{Compact: s.Point == core.CompactAfterTurn, KeepTail: 2}
}

// An embedded session compacts after a turn where the policy says to, as every
// other front end does. It used to compact only before a turn and on an
// oversized request, so a host that never looked at compaction could grow a
// session to the limit one turn at a time. The compaction rides the Prompt
// channel as compact_start and compact_end, before it closes.
func TestPromptCompactsAfterTheTurnWhenThePolicySaysSo(t *testing.T) {
	ag := coretest.NewAgent(replyClient{}, "m", "", core.Registry{}, core.WithCompactionPolicy(afterTurnOnly{}))
	seed := make([]provider.Message, 0, 8)
	for i := range 8 {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		seed = append(seed, provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: "filler"}}})
	}
	ag.SetMessages(seed)
	r := &Runtime{agent: ag}

	ch, err := r.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for ev := range ch {
		types = append(types, ev.Type)
		if ev.Error != "" {
			t.Errorf("%s carried an error: %s", ev.Type, ev.Error)
		}
	}
	n := len(types)
	if n < 2 || types[n-2] != "compact_start" || types[n-1] != "compact_end" {
		t.Fatalf("want the stream to end with compact_start, compact_end; got %v", types)
	}
	if got := len(ag.Messages()); got != 3 {
		t.Errorf("transcript has %d messages after the compaction, want the summary and a keep-tail of 2", got)
	}
}
