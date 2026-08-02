package core

import (
	"context"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The detector is only ever fed by the stream loop, so every test drives it
// through real prompts against a scripted provider — the usage rows arrive
// exactly the way production delivers them, ladder and all.

// cliffTurn is one dispatch's worth of events with a chosen cache shape.
func cliffTurn(input, cacheRead int) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventTextDelta{Delta: "ok"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: input, CacheReadTokens: cacheRead, OutputTokens: 20}},
		provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}},
	}
}

type cliffUsageStep struct{ input, cacheRead int }

// cliffAgent runs one scripted dispatch per step and records every CacheCliff
// event the run fires, in order.
func cliffAgent(t *testing.T, steps []cliffUsageStep) (*Agent, *[]CacheCliff, func(from, to int)) {
	t.Helper()
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		s := steps[n]
		return cliffTurn(s.input, s.cacheRead), nil
	}}
	a := cacheAwareAgent(t, client)
	a.SetPrefixDivergenceRecording(true)

	var mu sync.Mutex
	var events []CacheCliff
	a.AddCacheCliffObserver(func(cc CacheCliff) {
		mu.Lock()
		events = append(events, cc)
		mu.Unlock()
	})

	run := func(from, to int) {
		t.Helper()
		for i := from; i < to; i++ {
			if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
				t.Fatalf("Prompt %d returned %v", i, err)
			}
		}
	}
	return a, &events, run
}

// Three consecutive collapses on a large, append-only prompt is the provider
// signature (a measured session ran ~50 of them at ~$1 each); the third one
// must fire, and the event must carry the run so far — not just "something
// happened".
func TestCacheCliffFiresOnThreeConsecutiveCollapses(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 200, cacheRead: 60_000},   // warm: arms the detector, sets the baseline
		{input: 55_000, cacheRead: 9_728}, // collapse 1 — the shared floor
		{input: 58_000, cacheRead: 9_728}, // collapse 2
		{input: 60_000, cacheRead: 9_728}, // collapse 3 → fire
	}
	_, events, run := cliffAgent(t, steps)
	run(0, 3)
	if n := len(*events); n != 0 {
		t.Fatalf("fired after %d dispatches with only 2 collapses: %+v", 3, *events)
	}
	run(3, 4)
	if n := len(*events); n != 1 {
		t.Fatalf("got %d events, want exactly 1: %+v", n, *events)
	}
	got := (*events)[0]
	if !got.Ongoing || got.Dispatches != 3 {
		t.Errorf("event = %+v, want Ongoing with Dispatches=3", got)
	}
	// The waste is each collapse's input minus what the prompt genuinely grew:
	// (55000-4528) + (58000-3000) + (60000-2000).
	if want := 163_472; got.RereadTokens != want {
		t.Errorf("RereadTokens = %d, want %d", got.RereadTokens, want)
	}
}

// The note tracks a changing fact, so the run keeps reporting as it grows, and
// the first dispatch that hits cache again retracts with the zero event.
func TestCacheCliffRetractsOnRecovery(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 200, cacheRead: 60_000},
		{input: 55_000, cacheRead: 9_728},
		{input: 58_000, cacheRead: 9_728},
		{input: 60_000, cacheRead: 9_728}, // fire (3)
		{input: 61_000, cacheRead: 9_728}, // still on (4)
		{input: 300, cacheRead: 71_000},   // recovery → retract
	}
	_, events, run := cliffAgent(t, steps)
	run(0, 6)
	evs := *events
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3 (fire, grow, retract): %+v", len(evs), evs)
	}
	if evs[1].Dispatches != 4 || !evs[1].Ongoing {
		t.Errorf("second event = %+v, want the run grown to 4", evs[1])
	}
	last := evs[2]
	if last.Ongoing || last.Dispatches != 0 || last.RereadTokens != 0 {
		t.Errorf("retract event = %+v, want the zero CacheCliff", last)
	}
}

// A legitimate rebuild — here a model switch, the ladder's rung 0 — explains
// its own full-price re-read and must reset the run: two collapses, a rebuild,
// two more collapses is NOT a four-collapse cliff.
func TestCacheCliffResetsOnLegitimateRebuild(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 200, cacheRead: 60_000},
		{input: 55_000, cacheRead: 9_728},
		{input: 58_000, cacheRead: 9_728}, // 2 collapses — under threshold
		{input: 200, cacheRead: 60_000},   // dispatched under the NEW model: reset row
		{input: 55_000, cacheRead: 9_728},
		{input: 58_000, cacheRead: 9_728}, // 2 collapses again — still under
	}
	a, events, run := cliffAgent(t, steps)
	run(0, 3)
	a.SetModel("other-model")
	run(3, 6)
	if n := len(*events); n != 0 {
		t.Fatalf("got %d events, want none — the rebuild must reset the run: %+v", n, *events)
	}
}

// An endpoint that never reports cache reads (a local llama-style server)
// must never arm: every prompt is "uncached" there and always will be.
func TestCacheCliffNeverArmsWithoutCacheReads(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 100_000, cacheRead: 0},
		{input: 104_000, cacheRead: 0},
		{input: 108_000, cacheRead: 0},
		{input: 112_000, cacheRead: 0},
		{input: 116_000, cacheRead: 0},
	}
	_, events, run := cliffAgent(t, steps)
	run(0, 5)
	if n := len(*events); n != 0 {
		t.Fatalf("got %d events on a cacheless endpoint, want none: %+v", n, *events)
	}
}

// The detector leans on the prefix ladder to tell a collapse from a rebuild;
// with divergence recording off it must stay silent rather than guess.
func TestCacheCliffSilentWithRecordingOff(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 200, cacheRead: 60_000},
		{input: 55_000, cacheRead: 9_728},
		{input: 58_000, cacheRead: 9_728},
		{input: 60_000, cacheRead: 9_728},
	}
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		s := steps[n]
		return cliffTurn(s.input, s.cacheRead), nil
	}}
	a := cacheAwareAgent(t, client) // recording stays at core's zero value: off
	var events []CacheCliff
	a.AddCacheCliffObserver(func(cc CacheCliff) { events = append(events, cc) })
	for i := range steps {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d returned %v", i, err)
		}
	}
	if len(events) != 0 {
		t.Fatalf("got %d events with recording off, want none: %+v", len(events), events)
	}
}

// A cache WRITE is the provider building the cache, not dropping it — an
// Anthropic-style full write after an invalidation must not count as a
// collapse even though the read side is tiny.
func TestCacheCliffCountsWritesAsCacheWorking(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch n {
		case 0:
			return cliffTurn(200, 100_000), nil
		default:
			// read collapses, but the provider wrote the prompt back into
			// cache — the next dispatch will hit. Not an outage.
			return []provider.Event{
				provider.EventStart{Provider: "scripted"},
				provider.EventTextDelta{Delta: "ok"},
				provider.EventUsage{Usage: provider.Usage{InputTokens: 400, CacheReadTokens: 0, CacheWriteTokens: 104_000, OutputTokens: 20}},
				provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
					Role:    provider.RoleAssistant,
					Content: []provider.Content{provider.TextBlock{Text: "ok"}},
				}},
			}, nil
		}
	}}
	a := cacheAwareAgent(t, client)
	a.SetPrefixDivergenceRecording(true)
	var events []CacheCliff
	a.AddCacheCliffObserver(func(cc CacheCliff) { events = append(events, cc) })
	for i := 0; i < 4; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d returned %v", i, err)
		}
	}
	if len(events) != 0 {
		t.Fatalf("got %d events, want none — cache writes are the cache working: %+v", len(events), events)
	}
}

// On a six-figure prompt the tolerance shrinks to two: the boundary miss the
// base threshold exists for is exactly ONE dispatch, and waiting for a third
// collapse at that size costs another dollar-class re-read. A measured pair
// of ~200K misses cost $2.10 and fired nothing — this is that fix.
func TestCacheCliffFiresAtTwoOnHugePrompts(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 200, cacheRead: 150_000},   // warm: baseline over cliffBigPrompt
		{input: 140_000, cacheRead: 9_728}, // collapse 1 — tolerated (the boundary miss)
		{input: 145_000, cacheRead: 9_728}, // collapse 2 → fire
	}
	_, events, run := cliffAgent(t, steps)
	run(0, 2)
	if n := len(*events); n != 0 {
		t.Fatalf("fired on a single collapse — a reasoning model's boundary miss would false-positive: %+v", *events)
	}
	run(2, 3)
	evs := *events
	if len(evs) != 1 || !evs[0].Ongoing || evs[0].Dispatches != 2 {
		t.Fatalf("got %+v, want exactly one Ongoing event with Dispatches=2", evs)
	}
}

// One huge miss followed by recovery is the boundary-miss shape and must stay
// silent even above cliffBigPrompt.
func TestCacheCliffToleratesOneBoundaryMissOnHugePrompts(t *testing.T) {
	steps := []cliffUsageStep{
		{input: 200, cacheRead: 150_000},
		{input: 140_000, cacheRead: 9_728}, // the boundary miss
		{input: 400, cacheRead: 149_000},   // recovery
	}
	_, events, run := cliffAgent(t, steps)
	run(0, 3)
	if n := len(*events); n != 0 {
		t.Fatalf("got %d events, want none — one miss then recovery is normal on reasoning models: %+v", n, *events)
	}
}
