package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// recordingAsker stands in for the front end's question channel.
type recordingAsker struct {
	mu     sync.Mutex
	asked  []UserQuestion
	answer func(q UserQuestion) UserAnswer
}

// Ask answers the whole set positionally, so the fake keeps the Asker
// contract (one answer per question) that the real front ends keep.
func (r *recordingAsker) Ask(_ context.Context, qs []UserQuestion) ([]UserAnswer, error) {
	r.mu.Lock()
	r.asked = append(r.asked, qs...)
	fn := r.answer
	r.mu.Unlock()
	out := make([]UserAnswer, len(qs))
	for i, q := range qs {
		if fn == nil {
			out[i] = UserAnswer{Declined: true}
			continue
		}
		out[i] = fn(q)
	}
	return out, nil
}

// reloadedTool is a DIFFERENT advertised spec, which is the thing that matters:
// the registry's map key is terva's business, but the provider hashes the name,
// description and schema. Re-keying the same tool changes nothing it can see.
type reloadedTool struct{ recordingTool }

func (r *reloadedTool) Name() string        { return "grep" }
func (r *reloadedTool) Description() string { return "searches" }

func (r *recordingAsker) questions() []UserQuestion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]UserQuestion(nil), r.asked...)
}

func acceptsTheOffer() *recordingAsker {
	return &recordingAsker{answer: func(q UserQuestion) UserAnswer {
		return UserAnswer{Answer: q.Options[0]} // "Compact first"
	}}
}

// warmTurn is a normal turn that reports a large cache read — the state the
// guard exists for: a big prefix, currently being served for a tenth of what it
// would cost fresh.
func warmTurn(cacheRead int) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventTextDelta{Delta: "ok"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: 200, CacheReadTokens: cacheRead, OutputTokens: 20}},
		provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}},
	}
}

// guardAgent is an agent with the guard armed for real: cache-aware compaction
// on (without it the offer would have no saving behind it), a question channel,
// and enough transcript that a compaction has something to do.
func guardAgent(t *testing.T, cacheRead int, asker Asker) (*Agent, *scriptedClient) {
	t.Helper()
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		return warmTurn(cacheRead), nil
	}}
	a := cacheAwareAgent(t, client) // session (cache key), one tool, thinking on, cache-aware on
	a.SetPrefixChangeGuard(true)
	a.Asker = asker

	// AutoCompactKeepTail is 4, so a compaction needs more than four messages to
	// have anything to summarize — three turns.
	for _, p := range []string{"one", "two", "three"} {
		if err := a.PromptWithPolicy(context.Background(), p, nil, nil); err != nil {
			t.Fatalf("PromptWithPolicy(%q) returned %v", p, err)
		}
	}
	return a, client
}

// The headline case. A /model switch between turns silently invalidates the
// cached prompt: nothing breaks, nothing warns, and the next message quietly
// costs a full-price re-read of the whole conversation. The guard offers to
// condense it first — and compacts against the OUTGOING model, which still has
// the transcript cached.
func TestPrefixGuardOffersCompactionOnAModelSwitch(t *testing.T) {
	asker := acceptsTheOffer()
	a, client := guardAgent(t, 90_000, asker)
	before := len(client.calls())

	a.SetModel("cheaper-model")
	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}

	asked := asker.questions()
	if len(asked) != 1 {
		t.Fatalf("asked %d questions; want exactly 1", len(asked))
	}
	q := asked[0].Question
	if !strings.Contains(q, "the model changed") {
		t.Errorf("the question does not say what changed: %q", q)
	}
	if !strings.Contains(q, "90.0k") {
		t.Errorf("the question does not quote the cost about to be paid: %q", q)
	}

	// Turn, then the compaction, then the turn. The compaction is recognizable:
	// only the warm summarizer carries an ephemeral instruction.
	calls := client.calls()
	if len(calls) != before+2 {
		t.Fatalf("client saw %d new requests; want 2 (the compaction, then the turn)", len(calls)-before)
	}
	comp, turn := calls[before], calls[before+1]
	if comp.EphemeralContext == "" {
		t.Fatal("no compaction ran; the accepted offer did nothing")
	}
	if comp.Model != "warm-model" {
		t.Errorf("compacted on %q; want the OUTGOING model, which still has the transcript cached", comp.Model)
	}
	if turn.Model != "cheaper-model" {
		t.Errorf("the turn went to %q; want the incoming model", turn.Model)
	}
	// And the turn the user actually asked for went out on a condensed
	// transcript: the summary now leads it, in place of the history it replaced.
	if len(turn.Messages) == 0 {
		t.Fatal("the turn sent no messages")
	}
	head, _ := turn.Messages[0].Content[0].(provider.TextBlock)
	if !strings.Contains(head.Text, "Context Summary") {
		t.Errorf("the turn did not go out on the condensed transcript; it leads with %q", head.Text)
	}
}

// Many changes, one offer. An extension reload rewrites the tool set AND the
// system prompt that embeds it, and the user switches model too — three
// invalidations, one toll. Comparing prefixes rather than counting events is
// what makes this fall out with no coalescing logic to get wrong.
func TestPrefixGuardCoalescesEveryChangeIntoOneOffer(t *testing.T) {
	asker := acceptsTheOffer()
	a, _ := guardAgent(t, 90_000, asker)

	a.SetModel("cheaper-model")
	a.SetSystem("a reloaded system prompt")
	a.SetTools(Registry{"grep": &reloadedTool{}})

	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}

	asked := asker.questions()
	if len(asked) != 1 {
		t.Fatalf("asked %d questions; want exactly 1 — the invalidation is a single toll however many things caused it", len(asked))
	}
	q := asked[0].Question
	for _, want := range []string{"the model changed", "the tool set changed", "the system prompt changed"} {
		if !strings.Contains(q, want) {
			t.Errorf("the question omits %q, so the user can't see what it cost them: %q", want, q)
		}
	}
}

// A change that is reverted before the next turn withdraws its own offer. There
// is no pending-offer state to clean up: the live prefix simply compares equal
// to the dispatched one again.
func TestPrefixGuardIsSilentWhenAChangeIsReverted(t *testing.T) {
	asker := acceptsTheOffer()
	a, _ := guardAgent(t, 90_000, asker)

	a.SetModel("cheaper-model")
	a.SetModel("warm-model") // ...and back, before sending anything

	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	if n := len(asker.questions()); n != 0 {
		t.Errorf("asked %d questions; want 0 — the prefix is unchanged, so nothing was invalidated", n)
	}
}

// Decline, and the offer does not come back. It cannot: the turn dispatches on
// the new prefix, that becomes the last-sent one, and there is nothing left to
// ask about. The invalidation is a one-time toll, so no "already asked" flag is
// needed to stop a second offer — the toll being paid is what stops it.
func TestPrefixGuardOffersOnlyOncePerChange(t *testing.T) {
	declines := &recordingAsker{answer: func(UserQuestion) UserAnswer {
		return UserAnswer{Answer: "Send as-is"}
	}}
	a, client := guardAgent(t, 90_000, declines)
	before := len(client.calls())

	a.SetModel("cheaper-model")
	for _, p := range []string{"next", "and another"} {
		if err := a.PromptWithPolicy(context.Background(), p, nil, nil); err != nil {
			t.Fatalf("PromptWithPolicy(%q) returned %v", p, err)
		}
	}

	if n := len(declines.questions()); n != 1 {
		t.Errorf("asked %d questions across two turns; want 1 — the cost was paid on the first, so there is nothing to offer on the second", n)
	}
	// And declining means declining: no compaction ran.
	for _, req := range client.calls()[before:] {
		if req.EphemeralContext != "" {
			t.Error("a compaction ran despite the offer being declined")
		}
	}
}

// The cache has already expired, so the change is free. Warning anyway would be
// offering to save an expense the user avoided by going to lunch.
func TestPrefixGuardIsSilentOnceTheCacheHasExpired(t *testing.T) {
	asker := acceptsTheOffer()
	a, _ := guardAgent(t, 90_000, asker)

	a.mu.Lock()
	a.lastSent.sentAt = time.Now().Add(-prefixCacheTTL - time.Minute)
	a.mu.Unlock()

	a.SetModel("cheaper-model")
	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	if n := len(asker.questions()); n != 0 {
		t.Errorf("asked %d questions; want 0 — the prefix aged out of the cache, so the switch costs nothing extra", n)
	}
}

// The provider reported no cache reads at all, so there is no cache to lose.
// This is the honesty guard, and it is measured rather than hardcoded: it covers
// backends with no prefix cache, transcripts below the minimum cacheable size,
// and the reasoning models that re-read from every user-message boundary anyway
// — all of which would otherwise be told they were about to lose a cache they
// never had.
func TestPrefixGuardIsSilentWhenNothingWasCached(t *testing.T) {
	asker := acceptsTheOffer()
	a, _ := guardAgent(t, 0, asker)

	a.SetModel("cheaper-model")
	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	if n := len(asker.questions()); n != 0 {
		t.Errorf("asked %d questions; want 0 — nothing was served from cache, so nothing is lost", n)
	}
}

// A small cached prefix is not worth a blocking dialog. The guard's value is
// that it fires rarely and means something when it does.
func TestPrefixGuardIgnoresASmallCachedPrefix(t *testing.T) {
	asker := acceptsTheOffer()
	a, _ := guardAgent(t, prefixChangeMinTokens-1, asker)

	a.SetModel("cheaper-model")
	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	if n := len(asker.questions()); n != 0 {
		t.Errorf("asked %d questions; want 0 — below the threshold the tokens cost less than the interruption", n)
	}
}

// Without the cache-aware summarizer there is no saving to offer: compacting
// costs a full-price read of the whole transcript, which is the same full-price
// read that sending costs. The dialog would be asking the user to pay the bill
// twice in exchange for losing their conversation. So it must not appear.
func TestPrefixGuardStaysSilentWithoutCacheAwareCompaction(t *testing.T) {
	asker := acceptsTheOffer()
	a, _ := guardAgent(t, 90_000, asker)
	a.SetCacheAwareCompaction(false)

	a.SetModel("cheaper-model")
	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	if n := len(asker.questions()); n != 0 {
		t.Errorf("asked %d questions; want 0 — with a cold summarizer the offer saves nothing, so making it would be a lie", n)
	}
}

// No question channel — a one-shot run, a swarm child, the chat bridge. Skip the
// offer. Do NOT silently compact on their behalf: a guard that quietly discards
// the conversation to save money nobody asked it to save is a worse footgun than
// the one it guards against.
func TestPrefixGuardSkipsHostsWithNobodyToAsk(t *testing.T) {
	a, client := guardAgent(t, 90_000, nil)
	a.Asker = nil
	before := len(client.calls())

	a.SetModel("cheaper-model")
	if err := a.PromptWithPolicy(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	for _, req := range client.calls()[before:] {
		if req.EphemeralContext != "" {
			t.Error("compacted without asking, on a host that has no one to ask")
		}
	}
}
