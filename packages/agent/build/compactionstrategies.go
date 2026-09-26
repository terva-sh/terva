package build

import (
	"sync/atomic"

	"terva.sh/terva/packages/core"
)

// compactionSwitches are the live settings of terva's compaction features. The
// engine has a switch for none of them. A decision's Strategies names what a
// compaction may try, and a nil list is the cold summarizer alone
// (TKT-01M38V6KS): cache_aware_compaction and provider_compaction fill it. A
// decision at core.CompactPrefixChanged says whether to offer a compaction
// before a cache-invalidating change lands (TKT-01M3959W3):
// prefix_change_guard answers it. terva's policy reads these at every
// decision, so a settings toggle applies to the next one without rebuilding
// the agent.
type compactionSwitches struct {
	warm        atomic.Bool
	provider    atomic.Bool
	prefixGuard atomic.Bool
}

// strategies is the list the switches allow, in the engine's own order.
// Cold is always last: it is every path's fallback, and the one that works on
// every provider.
func (s *compactionSwitches) strategies() []core.CompactStrategy {
	var out []core.CompactStrategy
	if s.provider.Load() {
		out = append(out, core.CompactProvider)
	}
	if s.warm.Load() {
		out = append(out, core.CompactWarm)
	}
	return append(out, core.CompactCold)
}

var _ core.CompactionPolicy = CompactionPolicy{}

// Decide implements core.CompactionPolicy: the default decision for terva's
// live auto_compact mode, with the strategies its engine features allow
// whenever that decision leaves them to the policy, and the prefix-change
// offer when prefix_change_guard is on.
func (p CompactionPolicy) Decide(s core.CompactionState) core.CompactionDecision {
	d := p.DefaultCompactionPolicy.Decide(s)
	if p.switches == nil {
		return d
	}
	if d.Strategies == nil {
		d.Strategies = p.switches.strategies()
	}
	if s.Point == core.CompactPrefixChanged {
		d.Compact = p.switches.prefixGuard.Load()
	}
	return d
}

// setCompactionSwitch applies an engine feature to the switches of a's policy.
// An agent whose policy is not one NewAgent built has no switches, and the
// feature does nothing there, as it would for any host with its own policy.
func setCompactionSwitch(a *core.Agent, set func(*compactionSwitches)) {
	if p, ok := a.CompactionPolicy().(CompactionPolicy); ok && p.switches != nil {
		set(p.switches)
	}
}
