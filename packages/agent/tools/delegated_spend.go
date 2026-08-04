package tools

import (
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// liveDelegatedLine renders what sub-agents still in flight have spent so far,
// or "" when nothing is running or nothing has been reported yet.
//
// One renderer for both surfaces that report it — swarm_spawn's result and
// terva_status — because they answer the same question at two moments, and a
// coordinator comparing them must not find two differently-worded figures. The
// twin this repository keeps paying for is exactly this shape.
//
// Reports only when there is something to say. A zero total with agents running
// is the ordinary state a second after a spawn (no usage event has arrived yet),
// and printing "$0.0000 so far" there would read as a considered answer meaning
// "free" rather than "not yet known" — the misreading that made a subscription
// provider's zeros look deliberate.
func liveDelegatedLine(sw *swarm.Swarm) string {
	if sw == nil {
		return ""
	}
	u, n := sw.InFlightSpend()
	if n == 0 || u == (provider.Usage{}) {
		return ""
	}
	if u.CostUSD > 0 {
		return i18n.P("swarm.inflight.cost",
			"in flight: %d sub-agent(s) at work, $%.4f spent so far (finished ones move into the delegated total)",
			n, u.CostUSD)
	}
	// A provider that reports tokens without pricing them — a subscription, or a
	// backend that prices nothing. Tokens are the honest figure there, and
	// silence would be worse than an unpriced number.
	in := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
	if in == 0 && u.OutputTokens == 0 {
		return ""
	}
	return i18n.P("swarm.inflight.tokens",
		"in flight: %d sub-agent(s) at work, %s in / %s out so far, unpriced (finished ones move into the delegated total)",
		n, fmtTokens(in), fmtTokens(u.OutputTokens))
}

// spawnCostFooter is the line swarm_spawn appends after a successful spawn.
//
// The decision point is what makes this worth a line: the reviewed session's
// third spawn carried a `tier: medium` argument — a model reaching for a cost
// lever with no feedback on what the first two had cost. This is that feedback,
// and it arrives where the choice is actually made rather than in a per-turn
// note that would be read hundreds of times to be useful twice.
func spawnCostFooter(sw *swarm.Swarm) string {
	if line := liveDelegatedLine(sw); line != "" {
		return "\n\n" + line
	}
	return ""
}
