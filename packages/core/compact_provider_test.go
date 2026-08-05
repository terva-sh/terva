package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// What these tests pin: a server-side compaction must REPLACE the transcript
// with what the backend returned and nothing else, must never run when it was
// not asked for, and must fall back rather than fail — while leaving a row that
// says all three happened.
//
// The failure modes are quiet ones. A strategy that silently ran when it was
// off would hand a conversation an unportable checkpoint nobody chose; one that
// prepended anything to the backend's items would invalidate the very prefix it
// exists to preserve, and still look like it worked; one that reported no spend
// would read in the A/B as free.

// compactingClient is a scripted client that also speaks /compact. The two
// halves are scripted separately so a test can make the endpoint fail while the
// summarizer still works, which is the fallback case.
type compactingClient struct {
	*scriptedClient
	compact func(req provider.Request) ([]provider.Message, provider.Usage, error)

	compactCalls []provider.Request
}

func (c *compactingClient) CompactServerSide(_ context.Context, req provider.Request) ([]provider.Message, provider.Usage, error) {
	c.compactCalls = append(c.compactCalls, req)
	return c.compact(req)
}

// blobbed is what the codex endpoint returns: the user turns verbatim, the
// assistant turns gone, one encrypted summary standing in for them.
func blobbed(userTexts ...string) []provider.Message {
	var out []provider.Message
	for _, t := range userTexts {
		out = append(out, provider.Message{
			Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: t}}})
	}
	return append(out, provider.Message{
		Role: provider.RoleAssistant,
		Content: []provider.Content{provider.CompactionBlock{
			ID: "cmp_1", Encrypted: "gAAAAABopaque==", Provider: "scripted"}}})
}

// longReply is the shape of transcript this strategy is FOR: a short user turn
// and a large assistant one. That is not a convenience — it is the measured
// boundary. The endpoint returns every user turn verbatim and replaces only the
// assistant's, so what it reclaims is exactly the assistant's share, and a
// fixture built the other way round would trip the size floor and test the
// fallback instead of the feature.
var longReply = "## Goal\nthe client summarizer ran\n\n" +
	strings.Repeat("Then it went and did a great deal of work, at length, in detail. ", 60)

func serverCompactingAgent(t *testing.T, compact func(req provider.Request) ([]provider.Message, provider.Usage, error)) (*Agent, *compactingClient) {
	t.Helper()
	client := &compactingClient{
		scriptedClient: &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
			return saidText(longReply, 100), nil
		}},
		compact: compact,
	}
	a := cacheAwareAgent(t, client)
	a.SetProviderCompaction(true)
	return a, client
}

// The headline: with the feature on and a capable client, the backend produces
// the checkpoint, no summarizer is called, and what lands is exactly the
// backend's transcript with terva's divider appended after it.
func TestProviderCompactionReplacesTheTranscriptWithTheBackendsOwn(t *testing.T) {
	a, client := serverCompactingAgent(t, func(req provider.Request) ([]provider.Message, provider.Usage, error) {
		return blobbed("hello"), provider.Usage{InputTokens: 500, OutputTokens: 40, CostUSD: 0.01}, nil
	})

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	if res.Strategy != CompactProvider {
		t.Errorf("Strategy = %q; want %q", res.Strategy, CompactProvider)
	}
	// One Stream call, the turn itself. A second would mean a summarizer ran
	// anyway — the whole saving of this strategy is that none does.
	if calls := client.calls(); len(calls) != 1 {
		t.Errorf("client saw %d Stream requests; want 1 (the turn) — a summarizer ran despite the backend compacting", len(calls))
	}
	if len(client.compactCalls) != 1 {
		t.Fatalf("the /compact endpoint was called %d times; want 1", len(client.compactCalls))
	}

	msgs := a.Messages()
	if len(msgs) != 3 {
		t.Fatalf("transcript is %d messages; want the backend's 2 plus terva's divider: %+v", len(msgs), msgs)
	}
	// Located rather than indexed, so this reports the ordering it came to check
	// instead of tripping over a shifted position on the way there.
	blob, divider := -1, -1
	for i, m := range msgs {
		if m.Meta[MetaCompaction] == "true" {
			divider = i
		}
		for _, c := range m.Content {
			if _, ok := c.(provider.CompactionBlock); ok {
				blob = i
			}
		}
	}
	// The blob must survive into the live transcript. It is the ONLY encoding of
	// the assistant turns it replaced; terva cannot rebuild it.
	if blob < 0 {
		t.Fatalf("the compaction blob did not reach the transcript: %+v", msgs)
	}
	// Display surfaces render a MetaCompaction message as a divider rather than
	// as the user turn its role would imply. On this path it is the only thing
	// that says a compaction happened at all — the blob is opaque and the turns
	// around it are genuinely the user's.
	if divider < 0 {
		t.Fatalf("nothing in the transcript is marked as a compaction: %+v", msgs)
	}
	// Appended, never prepended. A message inserted in front of the backend's
	// items changes the prefix the provider cached, which is the one thing this
	// strategy exists to keep intact — and it would still look like it worked.
	if divider < blob || divider != len(msgs)-1 {
		t.Errorf("terva's divider is at %d and the blob at %d, in a transcript of %d; the divider must come LAST, "+
			"because anything inserted ahead of the backend's items invalidates the cached prefix this strategy exists to preserve",
			divider, blob, len(msgs))
	}

	// The spend is real money on a transcript-sized read. Reported as zero it
	// would make the A/B conclude this arm is free.
	if res.Usage.InputTokens != 500 || res.Usage.CostUSD == 0 {
		t.Errorf("Usage = %+v; want the compaction call's own spend", res.Usage)
	}
	// And it has to reach the session's running total, not just the row. The
	// client summarizers fold every attempt in as they go (drainSummary); a
	// strategy that skipped that step would make a conversation's compactions
	// look free in the one place a user actually watches the money.
	if got := a.Cost(); got.InputTokens < 500 || got.CostUSD < 0.01 {
		t.Errorf("session total = %+v; the compaction's own spend never reached the cost tracker", got)
	}
	if res.SupersededMessages != 2 {
		t.Errorf("SupersededMessages = %d; want 2 — the row is the only thing that says how big the replaced transcript was", res.SupersededMessages)
	}
	if res.Summary == "" {
		t.Error("Summary is empty; hosts render it, and a blank one reads as a bug rather than as an encrypted checkpoint")
	}
}

// The compaction request must ride the SAME prefix the conversation has been
// running on. Everything about this strategy is a bet on the provider's prefix
// match; a compaction sent under a different model, system prompt or cache
// route is a cold, transcript-sized read wearing the label of a cheap one.
func TestProviderCompactionReusesTheDispatchedPrefix(t *testing.T) {
	a, client := serverCompactingAgent(t, func(req provider.Request) ([]provider.Message, provider.Usage, error) {
		return blobbed("hello"), provider.Usage{}, nil
	})
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatal(err)
	}

	turn := client.calls()[0]
	comp := client.compactCalls[0]
	if comp.Model != turn.Model {
		t.Errorf("compaction model = %q; want %q", comp.Model, turn.Model)
	}
	if comp.System != turn.System {
		t.Errorf("compaction system = %q; want the dispatched %q", comp.System, turn.System)
	}
	if comp.PromptCacheKey == "" || comp.PromptCacheKey != turn.PromptCacheKey {
		t.Errorf("compaction cache key = %q; want %q — right bytes, wrong route, full price", comp.PromptCacheKey, turn.PromptCacheKey)
	}
}

// Off is off, and a client that cannot compact server-side is left alone.
//
// Both halves matter and neither is redundant. The first is the whole reason
// the strategy ships behind a toggle — its checkpoint is not portable, and
// nobody gets one they did not ask for. The second is what a capability probe
// is FOR: every other provider must be untouched by this existing.
func TestProviderCompactionOnlyRunsWhenItIsBothOnAndAvailable(t *testing.T) {
	t.Run("feature off, capable client", func(t *testing.T) {
		a, client := serverCompactingAgent(t, func(req provider.Request) ([]provider.Message, provider.Usage, error) {
			t.Error("the backend was asked to compact while the feature was off")
			return blobbed("hello"), provider.Usage{}, nil
		})
		a.SetProviderCompaction(false)
		if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
			t.Fatal(err)
		}
		res, err := a.Compact(context.Background(), 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Strategy == CompactProvider {
			t.Error("Strategy is provider with the feature off")
		}
		if len(client.compactCalls) != 0 {
			t.Errorf("/compact called %d times with the feature off", len(client.compactCalls))
		}
	})

	t.Run("feature on, incapable client", func(t *testing.T) {
		// A plain scriptedClient: no CompactServerSide method at all, which is
		// every provider but codex.
		client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
			return saidText("## Goal\nship it", 100), nil
		}}
		a := cacheAwareAgent(t, client)
		a.SetProviderCompaction(true)
		if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
			t.Fatal(err)
		}
		res, err := a.Compact(context.Background(), 0, nil)
		if err != nil {
			t.Fatalf("compaction failed on a client with no /compact endpoint: %v", err)
		}
		if res.Strategy != CompactWarm {
			t.Errorf("Strategy = %q; want %q — an unavailable capability must be invisible, not a fallback", res.Strategy, CompactWarm)
		}
		if res.FallbackReason != "" {
			t.Errorf("FallbackReason = %q; a provider that never offered the endpoint did not fall back from it", res.FallbackReason)
		}
	})
}

// A backend that refuses must cost the conversation nothing but money.
//
// A compaction usually runs because the context window has already filled, so
// refusing outright strands the session at the worst possible moment. What the
// row has to keep is BOTH halves: that the cheap path was tried and declined,
// and what the expensive one that finished then cost.
func TestProviderCompactionFallsBackAndRecordsBothHalves(t *testing.T) {
	a, client := serverCompactingAgent(t, func(req provider.Request) ([]provider.Message, provider.Usage, error) {
		// Billed before it failed: the endpoint read the transcript, then the
		// reply was unusable. Spend that vanishes here vanishes from the A/B.
		return nil, provider.Usage{InputTokens: 400, CostUSD: 0.005}, errors.New("boom")
	})
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("a failed server-side compaction must fall back, not fail: %v", err)
	}

	// The warm summarizer finished it, and the name says both things.
	if want := providerFellBackTo(CompactWarm); res.Strategy != want {
		t.Errorf("Strategy = %q; want %q", res.Strategy, want)
	}
	if !strings.HasPrefix(string(res.Strategy), "provider_fallback_") {
		t.Errorf("Strategy %q does not say the backend was tried at all", res.Strategy)
	}
	if !strings.Contains(res.FallbackReason, "provider_error") {
		t.Errorf("FallbackReason = %q; want the server-side reason in it", res.FallbackReason)
	}
	if res.Summary == "" || !strings.Contains(res.Summary, "client summarizer") {
		t.Errorf("the fallback summary is not the client one: %q", res.Summary)
	}
	// Both attempts were billed, so both are in the total. The summarizer's own
	// 100 input tokens plus the endpoint's 400.
	if res.Usage.InputTokens != 500 {
		t.Errorf("Usage.InputTokens = %d; want 500 — the failed attempt's spend was dropped", res.Usage.InputTokens)
	}
	if len(client.calls()) != 2 {
		t.Errorf("client saw %d Stream requests; want 2 (the turn, then the fallback summarizer)", len(client.calls()))
	}
}

// A compaction that reclaims nothing must be refused, because the alternative
// is not a disappointing result — it is a LOOP.
//
// Auto-compact fires on a context fraction. A checkpoint that leaves the
// fraction where it was fires another one on the next turn, and another, each
// paying for a transcript-sized read. Measured, this is not hypothetical: the
// endpoint returns every user turn verbatim, so on a chat-heavy transcript the
// result can be as large as its input — on a 4-turn fixture it was LARGER.
func TestProviderCompactionRefusesAResultThatReclaimsNothing(t *testing.T) {
	a, _ := serverCompactingAgent(t, func(req provider.Request) ([]provider.Message, provider.Usage, error) {
		// Everything back, verbatim, plus a blob: bigger than what went in.
		return append(append([]provider.Message(nil), req.Messages...), provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.CompactionBlock{
				ID: "cmp_1", Encrypted: strings.Repeat("A", 512), Provider: "scripted"}},
		}), provider.Usage{}, nil
	})
	if err := a.Prompt(context.Background(), "hello, this is a reasonably long user turn to give the transcript some size", nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy == CompactProvider {
		t.Fatal("kept a compaction that reclaimed nothing; auto-compact will now fire on every turn")
	}
	if !strings.Contains(res.FallbackReason, "provider_reclaimed_too_little") {
		t.Errorf("FallbackReason = %q; want it to name the size floor rather than a generic error", res.FallbackReason)
	}
}

// The executed-actions ledger has to survive a server-side compaction, and it
// matters MORE here than on the client paths.
//
// Those keep a tail of recent turns verbatim, so the last few tool calls stay
// visible in the transcript. This one keeps none: the backend drops every
// assistant turn, so every call terva made is inside an opaque blob. Without
// the ledger a resuming agent has no evidence of what already ran, and mid-turn
// that means re-running a side effect it has already had.
func TestProviderCompactionCarriesTheExecutedActionsLedger(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "do it"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: longReply},
			provider.ToolCallBlock{ID: "call_1", Name: "echo", Arguments: []byte(`{"text":"hi"}`)}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.ToolResultBlock{
			CallID: "call_1", Content: []provider.Content{provider.TextBlock{Text: "ok"}}}}},
	}
	next, res, err := compactViaProvider(context.Background(),
		&stubCompactor{out: blobbed("do it")}, promptPrefix{model: "warm-model"}, msgs, nil, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != CompactProvider {
		t.Fatalf("Strategy = %q", res.Strategy)
	}
	last := next[len(next)-1]
	text := ""
	for _, c := range last.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			text += tb.Text
		}
	}
	if !strings.Contains(text, "echo") {
		t.Errorf("the divider carries no record of the executed call:\n%s", text)
	}
	// The ledger must be over the WHOLE transcript, not a keepTail-trimmed
	// prefix of it: nothing survives verbatim on this path, so there is no tail
	// whose calls are still visible to the resuming agent.
	if res.SupersededMessages != len(msgs) {
		t.Errorf("SupersededMessages = %d; want all %d", res.SupersededMessages, len(msgs))
	}
}

type stubCompactor struct {
	out   []provider.Message
	usage provider.Usage
	err   error
}

func (s *stubCompactor) CompactServerSide(context.Context, provider.Request) ([]provider.Message, provider.Usage, error) {
	return s.out, s.usage, s.err
}
