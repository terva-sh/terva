package swarm

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// encodeCumulativeLikeAChild builds the `cumulative` map exactly the way a terva
// child produces it: through core.UsageToWire, then JSON, then back into a
// map[string]any the way the event decoder sees it.
//
// This is the point of the test. A hand-written fixture encodes whatever field
// names its author believed in, so it agrees with a decoder that believes the
// same wrong thing — which is precisely how this shipped. The only fixture that
// existed used the right key but asserted only CostUSD, so the token fields were
// never compared at all.
func encodeCumulativeLikeAChild(t *testing.T, u provider.Usage) map[string]any {
	t.Helper()
	b, err := json.Marshal(core.UsageToWire(u))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// costFromEvent used to read provider.Usage's SESSION-ROW json tags
// (input_tokens, output_tokens, cache_read_tokens, cache_write_tokens) off a
// payload the child encodes with core.UsageToWire, whose tags are input, output,
// cache_read, cache_write. No terva child has ever emitted the vocabulary it was
// reading, so every delegated token count was a hard zero.
func TestDelegatedUsageSurvivesTheRealChildEncoding(t *testing.T) {
	want := provider.Usage{
		InputTokens:      250000,
		OutputTokens:     500,
		CacheReadTokens:  9000,
		CacheWriteTokens: 1200,
		CostUSD:          1.25,
	}
	ev := Event{Type: "usage", Data: map[string]any{
		"cumulative": encodeCumulativeLikeAChild(t, want),
	}}

	got, ok := costFromEvent(ev)
	if !ok {
		t.Fatal("costFromEvent reported no usage for a well-formed child usage event")
	}
	if got.InputTokens != want.InputTokens {
		t.Errorf("InputTokens = %d, want %d — the decoder is reading a key the child does not emit",
			got.InputTokens, want.InputTokens)
	}
	if got.OutputTokens != want.OutputTokens {
		t.Errorf("OutputTokens = %d, want %d", got.OutputTokens, want.OutputTokens)
	}
	if got.CacheReadTokens != want.CacheReadTokens {
		t.Errorf("CacheReadTokens = %d, want %d", got.CacheReadTokens, want.CacheReadTokens)
	}
	if got.CacheWriteTokens != want.CacheWriteTokens {
		t.Errorf("CacheWriteTokens = %d, want %d", got.CacheWriteTokens, want.CacheWriteTokens)
	}
	if got.CostUSD != want.CostUSD {
		t.Errorf("CostUSD = %v, want %v", got.CostUSD, want.CostUSD)
	}
}

// The subscription case is the one that lost EVERYTHING. A subscription-backed
// child reports cost_usd 0, so with the token fields decoding to zero the whole
// Usage was the zero value, costFromEvent's `u != provider.Usage{}` guard failed,
// and the child's entire spend was never recorded — the exact failure delegated
// usage exists to prevent.
func TestSubscriptionChildSpendIsRecordedWithoutADollarFigure(t *testing.T) {
	want := provider.Usage{InputTokens: 250000, OutputTokens: 500, CostUSD: 0}
	ev := Event{Type: "usage", Data: map[string]any{
		"cumulative": encodeCumulativeLikeAChild(t, want),
	}}

	got, ok := costFromEvent(ev)
	if !ok {
		t.Fatal("a subscription-backed child's spend was dropped entirely: zero dollars plus zero-decoded " +
			"tokens makes the whole Usage the zero value, and the non-empty guard then discards it")
	}
	if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens {
		t.Errorf("usage = %+v, want input=%d output=%d", got, want.InputTokens, want.OutputTokens)
	}
}

// A usage event with no readable numbers must still be ignored rather than
// booked as a zero-cost turn — the behaviour the non-empty guard was written for,
// kept intact by the fix.
func TestEmptyCumulativeIsStillIgnored(t *testing.T) {
	ev := Event{Type: "usage", Data: map[string]any{"cumulative": map[string]any{}}}
	if _, ok := costFromEvent(ev); ok {
		t.Error("an empty cumulative block should not register as usage")
	}
}
