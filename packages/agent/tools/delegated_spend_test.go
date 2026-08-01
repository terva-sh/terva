package tools

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
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
		swarm.IngestEvent(swarm.Event{Type: "usage", Data: map[string]any{
			"cumulative": map[string]any{
				"input_tokens":       float64(u.InputTokens),
				"output_tokens":      float64(u.OutputTokens),
				"cache_read_tokens":  float64(u.CacheReadTokens),
				"cache_write_tokens": float64(u.CacheWriteTokens),
				"cost_usd":           u.CostUSD,
			},
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
	if !strings.Contains(line, "not yet in this session's delegated total") {
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
