package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// runningSwarm returns a swarm with one agent blocked mid-run, plus a way to
// report spend against it.
//
// Spend is fed through swarm.IngestEvent with a real `usage` event rather than a
// test-only setter, because the property under test is that a child's LIVE
// cumulative — the thing the runner already records on every usage event — is
// what these surfaces read. A backdoor setter would pass just as well against a
// figure nothing actually produces.
func runningSwarm(t *testing.T) (*swarm.Swarm, func(provider.Usage)) {
	t.Helper()
	root := testsupport.TempDir(t)
	f := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, _ swarm.Sink) error {
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
	t.Cleanup(f.StopAll)
	a, err := f.Spawn(context.Background(), "the task")
	if err != nil {
		t.Fatal(err)
	}
	return f, func(u provider.Usage) {
		// Encode the cumulative block through core.UsageToWire — the serializer
		// a real child uses — rather than naming keys here.
		//
		// This fixture used to hand-write "input_tokens", "output_tokens",
		// "cache_read_tokens" and "cache_write_tokens", which are
		// provider.Usage's SESSION-ROW tags. The wire tags are "input",
		// "output", "cache_read", "cache_write". The decoder read the same
		// wrong names, so fixture and code agreed and every one of these tests
		// passed while every delegated token count in the product was zero.
		//
		// A fixture that encodes the same assumption as the code asserts
		// nothing. Going through the real encoder is what makes these tests
		// evidence.
		b, err := json.Marshal(core.UsageToWire(u))
		if err != nil {
			t.Fatal(err)
		}
		var cumulative map[string]any
		if err := json.Unmarshal(b, &cumulative); err != nil {
			t.Fatal(err)
		}
		swarm.IngestEvent(swarm.Event{Type: "usage", Data: map[string]any{
			"cumulative": cumulative,
		}}, nil, nil, a)
	}
}

// The line reports a real figure and says plainly that it is NOT in the
// session's delegated total — a reader who sees both numbers must not add them,
// and must not wonder why they disagree.
func TestLiveDelegatedLineReportsCost(t *testing.T) {
	sw, spend := runningSwarm(t)
	spend(provider.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 15.63})

	line := liveDelegatedLine(sw)
	if !strings.Contains(line, "15.63") {
		t.Errorf("line does not carry the figure: %q", line)
	}
	if !strings.Contains(line, "1 sub-agent") {
		t.Errorf("line does not say how many are running: %q", line)
	}
	if !strings.Contains(line, "finished ones move into the delegated total") {
		t.Errorf("line does not disclaim double-counting: %q", line)
	}
}

// Silence beats "$0.0000". A zero total with an agent running is the ordinary
// state a second after a spawn — no usage event has arrived — and a printed zero
// reads as a considered answer meaning "free" rather than "not yet known".
func TestLiveDelegatedLineSaysNothingBeforeAnyUsage(t *testing.T) {
	sw, _ := runningSwarm(t)
	if line := liveDelegatedLine(sw); line != "" {
		t.Errorf("reported before any usage arrived: %q", line)
	}
	if line := liveDelegatedLine(nil); line != "" {
		t.Errorf("nil swarm reported: %q", line)
	}
}

// A subscription or an unpriced backend reports tokens without dollars. Silence
// there would hide real spend, so the tokens are reported and labelled unpriced
// rather than dressed up as $0.
func TestLiveDelegatedLineFallsBackToTokens(t *testing.T) {
	sw, spend := runningSwarm(t)
	spend(provider.Usage{InputTokens: 120_000, OutputTokens: 4_000})

	line := liveDelegatedLine(sw)
	if line == "" {
		t.Fatal("an unpriced provider's real token spend was reported as nothing")
	}
	if !strings.Contains(line, "unpriced") {
		t.Errorf("line does not say the figure is unpriced: %q", line)
	}
	if strings.Contains(line, "$") {
		t.Errorf("line invented a dollar figure: %q", line)
	}
}

// The spawn footer is the decision point: the reviewed session's third spawn
// carried a cost-lever argument with no feedback on what the first two cost.
func TestSpawnCostFooterAppearsOnlyWhenThereIsSpend(t *testing.T) {
	sw, spend := runningSwarm(t)
	if got := spawnCostFooter(sw); got != "" {
		t.Errorf("footer before any spend: %q", got)
	}
	spend(provider.Usage{CostUSD: 4.50})
	got := spawnCostFooter(sw)
	if !strings.HasPrefix(got, "\n\n") {
		t.Errorf("footer should be separated from the spawn body: %q", got)
	}
	if !strings.Contains(got, "4.50") {
		t.Errorf("footer does not carry the figure: %q", got)
	}
}
