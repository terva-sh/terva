package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The invariant these tests pin: the cache-aware summarizer must reproduce the
// dispatched prefix EXACTLY, and must fall back cleanly when it can't be used.
//
// Prefix fidelity has no natural failure signal. Send a system prompt one byte
// off, drop the tools array, flip the thinking config, and everything still
// works — the summary comes back, the transcript condenses, nothing errors. The
// only symptom is that the user is quietly billed full price for a 90k-token
// read they were told would be served from cache. A test is the only thing
// standing between "cache-aware" and a comforting label on an identical bill.

// scriptedClient answers request n with whatever the script returns for it, and
// records every request it was handed.
type scriptedClient struct {
	name   string
	script func(n int, req provider.Request) ([]provider.Event, error)

	mu   sync.Mutex
	reqs []provider.Request
}

func (c *scriptedClient) Name() string { return c.name }

func (c *scriptedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	n := len(c.reqs)
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()

	evs, err := c.script(n, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event, len(evs))
	go func() {
		defer close(out)
		for _, e := range evs {
			out <- e
		}
	}()
	return out, nil
}

func (c *scriptedClient) calls() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.reqs...)
}

// saidText is a plain text answer with a usage row.
func saidText(text string, input int) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventTextDelta{Delta: text},
		provider.EventUsage{Usage: provider.Usage{InputTokens: input, OutputTokens: 10}},
		provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		}},
	}
}

// calledATool is the awkward answer the warm path has to survive: the model's
// tools are still advertised (withdrawing them would bust the cache we came
// for), so it can decide to use one instead of writing the summary. No text
// comes back at all.
func calledATool(input int) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: input, OutputTokens: 10}},
		provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{}`),
			}},
		}},
	}
}

// cacheAwareAgent wires an agent with a real session (so PromptCacheKey is
// populated), one tool, and a thinking level — everything that has to survive
// into the warm request unchanged.
func cacheAwareAgent(t *testing.T, client provider.Client) *Agent {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/ws", "p", "warm-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	a := NewAgent(client, "warm-model", "you are terva", Registry{"echo": &recordingTool{}})
	a.Reasoning = "medium"
	a.AdoptSessionIdentity(sess)
	a.SetCacheAwareCompaction(true)
	return a
}

// The whole feature, in one assertion: the summarization request must carry the
// same model, system, tools, thinking config and cache route as the turn before
// it — because a provider's cache is a prefix MATCH, and a single differing
// byte anywhere in that prefix means the transcript behind it is re-read at
// full price.
func TestCacheAwareCompactionReproducesTheWarmPrefix(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		return saidText("## Goal\nship it", 100), nil
	}}
	a := cacheAwareAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}

	calls := client.calls()
	if len(calls) != 2 {
		t.Fatalf("client saw %d requests; want 2 (the turn, then the warm compaction)", len(calls))
	}
	turn, comp := calls[0], calls[1]

	if comp.Model != turn.Model {
		t.Errorf("compaction model = %q; want %q — a model switch invalidates every cache tier", comp.Model, turn.Model)
	}
	if comp.System != turn.System {
		t.Errorf("compaction system prompt differs from the dispatched one;\n got: %q\nwant: %q", comp.System, turn.System)
	}
	if !reflect.DeepEqual(comp.Tools, turn.Tools) {
		t.Errorf("compaction tools = %+v; want %+v — the tools array renders FIRST, so dropping it invalidates system and messages too", comp.Tools, turn.Tools)
	}
	if len(comp.Tools) == 0 {
		t.Error("compaction advertised no tools; the fixture registers one, so this is the 'we don't need tools to summarize' optimization that costs the entire cache")
	}
	if comp.Reasoning != turn.Reasoning {
		t.Errorf("compaction reasoning = %q; want %q — a thinking-config change invalidates the cached MESSAGE blocks, which is the expensive tier", comp.Reasoning, turn.Reasoning)
	}
	if comp.PromptCacheKey == "" || comp.PromptCacheKey != turn.PromptCacheKey {
		t.Errorf("compaction cache key = %q; want %q — right bytes, wrong route, full price", comp.PromptCacheKey, turn.PromptCacheKey)
	}

	// The ask rides the ephemeral tail, which lands AFTER the cache breakpoint
	// and carries no cache_control. In the system prompt it would have
	// invalidated the system and message tiers — costing exactly what it saves.
	if comp.EphemeralContext == "" {
		t.Error("the summarization instruction is not in EphemeralContext; if it moved into System, the request pays for what it was trying to save")
	}
	if !strings.Contains(comp.EphemeralContext, "## Goal") {
		t.Errorf("EphemeralContext does not carry the summary format: %q", comp.EphemeralContext)
	}

	// And the transcript is the conversation's own messages, not a flattened
	// retelling of them — that reframing is what makes the cold path cold.
	if len(comp.Messages) != 2 {
		t.Fatalf("compaction sent %d messages; want 2 (the turn's user message and the assistant reply, as cached)", len(comp.Messages))
	}
	if !reflect.DeepEqual(comp.Messages[0], turn.Messages[0]) {
		t.Errorf("the first message differs from the cached one, so the prefix match breaks at message 0:\n got: %#v\nwant: %#v", comp.Messages[0], turn.Messages[0])
	}
}

// The A/B cannot be run at all if the log won't say which arm produced a row,
// and the failure this feature is most exposed to is silent: a warm compaction
// whose prefix match missed looks exactly like one that hit — same summary, same
// transcript, no error — differing only in that the tokens were billed at full
// price. Recording the arm is what turns "the cache hit" into a checkable claim.
func TestCompactionRecordsWhichSummarizerRan(t *testing.T) {
	summarizes := func(n int, req provider.Request) ([]provider.Event, error) {
		return saidText("## Goal\nship it", 100), nil
	}

	t.Run("warm", func(t *testing.T) {
		a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: summarizes})
		if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
			t.Fatal(err)
		}
		res, err := a.Compact(context.Background(), 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Strategy != CompactWarm {
			t.Errorf("Strategy = %q; want %q", res.Strategy, CompactWarm)
		}
		if res.FallbackReason != "" {
			t.Errorf("FallbackReason = %q; want empty on a clean warm run", res.FallbackReason)
		}
	})

	t.Run("cold", func(t *testing.T) {
		a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: summarizes})
		a.SetCacheAwareCompaction(false)
		if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
			t.Fatal(err)
		}
		res, err := a.Compact(context.Background(), 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Strategy != CompactCold {
			t.Errorf("Strategy = %q; want %q", res.Strategy, CompactCold)
		}
	})

	// The fallback reason is not decoration. "tool_use" is a PROMPTING failure —
	// the model had its tools live and chose one over summarizing, which better
	// instructions might fix. "rejected_too_large" is a SIZE failure, structural
	// to a warm request being larger than a cold one, which they cannot. The A/B
	// has to be able to tell those apart, or a fallback rate is just a number.
	t.Run("fallback says why", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			warm   func() ([]provider.Event, error)
			reason string
		}{
			{"tool_use", func() ([]provider.Event, error) { return calledATool(5_000), nil }, "tool_use"},
			{"rejected", func() ([]provider.Event, error) {
				return nil, provider.NewHTTPError("scripted", 413, "", "prompt is too long")
			}, "rejected_too_large"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				a := cacheAwareAgent(t, &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
					switch n {
					case 0:
						return saidText("working", 100), nil
					case 1:
						return tc.warm()
					default:
						return saidText("## Goal\ncold summary", 90_000), nil
					}
				}})
				if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
					t.Fatal(err)
				}
				res, err := a.Compact(context.Background(), 0, nil)
				if err != nil {
					t.Fatal(err)
				}
				if res.Strategy != CompactWarmFellBack {
					t.Errorf("Strategy = %q; want %q", res.Strategy, CompactWarmFellBack)
				}
				if res.FallbackReason != tc.reason {
					t.Errorf("FallbackReason = %q; want %q", res.FallbackReason, tc.reason)
				}
			})
		}
	})
}

// keepTail must NOT be pre-sliced off the warm request. Truncating the
// transcript hands the provider a shorter prefix and moves the cache
// breakpoint; the tail is already cached, so sending it is free. Which messages
// survive is a local decision, and the model is simply told about it.
func TestCacheAwareCompactionDoesNotPreSliceTheTail(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		return saidText("## Goal\nship it", 100), nil
	}}
	a := cacheAwareAgent(t, client)

	for _, p := range []string{"one", "two", "three"} {
		if err := a.Prompt(context.Background(), p, nil, nil); err != nil {
			t.Fatalf("Prompt(%q) returned %v", p, err)
		}
	}
	full := len(a.Messages()) // 3 user + 3 assistant

	if _, err := a.Compact(context.Background(), 2, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}

	calls := client.calls()
	comp := calls[len(calls)-1]
	if len(comp.Messages) != full {
		t.Errorf("compaction sent %d of %d messages; want all of them — a truncated transcript is a prefix the provider never cached", len(comp.Messages), full)
	}
	if !strings.Contains(comp.EphemeralContext, "2 most recent") {
		t.Errorf("the model was not told which messages survive verbatim: %q", comp.EphemeralContext)
	}
}

// The model answers the summarization ask with a tool call — entirely possible,
// because its tools are still live — and produces no summary text. The
// compaction must not fail: it falls back to the bespoke path and completes.
func TestCacheAwareCompactionFallsBackWhenTheModelCallsATool(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch n {
		case 0:
			return saidText("working on it", 100), nil
		case 1:
			return calledATool(5_000), nil // the warm attempt, refusing to summarize
		default:
			return saidText("## Goal\nthe cold summary", 90_000), nil
		}
	}}
	a := cacheAwareAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Compact returned %v; a tool_use answer must fall back, not fail", err)
	}
	if !strings.Contains(res.Summary, "the cold summary") {
		t.Errorf("summary = %q; want the cold path's", res.Summary)
	}

	calls := client.calls()
	if len(calls) != 3 {
		t.Fatalf("client saw %d requests; want 3 (turn, warm attempt, cold fallback)", len(calls))
	}
	cold := calls[2]
	if len(cold.Tools) != 0 {
		t.Errorf("the fallback advertised %d tools; the bespoke path must advertise none", len(cold.Tools))
	}
	if cold.System == a.System {
		t.Error("the fallback reused the agent's system prompt; the bespoke path has its own summarizer system prompt")
	}
	if len(cold.Messages) != 1 {
		t.Errorf("the fallback sent %d messages; the bespoke path flattens the transcript into one", len(cold.Messages))
	}

	// The ledger has to carry BOTH attempts. a.cost folded each into the
	// cumulative total as it streamed, and SessionUsageDetail subtracts this
	// row's usage back out of the last-turn delta — so a CompactResult that
	// under-reports what was spent leaks the difference into the context gauge.
	if got, want := res.Usage.InputTokens, 95_000; got != want {
		t.Errorf("CompactResult.Usage.InputTokens = %d; want %d (the failed warm attempt's %d plus the cold read's %d — both were billed)",
			got, want, 5_000, 90_000)
	}
}

// The warm attempt fails outright. The likeliest cause is the one that hurts:
// the warm request re-sends the whole transcript PLUS the live system and tools,
// so it is slightly larger than the flattened cold prompt — and a compaction
// triggered by a context-overflow 413 is exactly the one most likely to overflow
// again. Falling back is the design, not the safety net.
func TestCacheAwareCompactionFallsBackWhenTheWarmRequestIsRejected(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch n {
		case 0:
			return saidText("working on it", 100), nil
		case 1:
			return nil, provider.NewHTTPError("scripted", 413, "", "prompt is too long")
		default:
			return saidText("## Goal\nthe cold summary", 90_000), nil
		}
	}}
	a := cacheAwareAgent(t, client)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Compact returned %v; a rejected warm request must fall back, not fail", err)
	}
	if !strings.Contains(res.Summary, "the cold summary") {
		t.Errorf("summary = %q; want the cold path's", res.Summary)
	}
	if n := len(client.calls()); n != 3 {
		t.Fatalf("client saw %d requests; want 3 (turn, rejected warm attempt, cold fallback)", n)
	}
}

// The off switch, at the core level: an agent with the feature off gets the
// bespoke summarizer — its own system prompt, no tools, the transcript flattened
// into one block. This is the way back for anyone who wants the dedicated
// summarizer prompt rather than a summary written from inside the agent persona.
func TestCompactionIsColdWhenTheFeatureIsOff(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		return saidText("## Goal\nship it", 100), nil
	}}
	a := cacheAwareAgent(t, client)
	a.SetCacheAwareCompaction(false)

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}

	calls := client.calls()
	if len(calls) != 2 {
		t.Fatalf("client saw %d requests; want 2", len(calls))
	}
	if comp := calls[1]; len(comp.Tools) != 0 || len(comp.Messages) != 1 {
		t.Errorf("the default compaction was not the bespoke one: %d tools, %d messages", len(comp.Tools), len(comp.Messages))
	}
}

// Cache-aware compaction on an agent that has never dispatched — a session
// resumed from disk and compacted before its first turn — has no warm prefix to
// be aware of. It must not pretend otherwise: there is nothing cached, so the
// bespoke path (smaller, and framed for summarization) is strictly better.
func TestCacheAwareCompactionNeedsAWarmPrefix(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		return saidText("## Goal\nship it", 100), nil
	}}
	a := cacheAwareAgent(t, client)
	a.SetMessages([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "resumed from disk"}},
	}})

	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}

	calls := client.calls()
	if len(calls) != 1 {
		t.Fatalf("client saw %d requests; want 1", len(calls))
	}
	if comp := calls[0]; len(comp.Tools) != 0 || comp.EphemeralContext != "" {
		t.Errorf("compacted against a prefix that was never dispatched: %d tools, ephemeral %q", len(comp.Tools), comp.EphemeralContext)
	}
}
