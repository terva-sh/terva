package tools

// The rigor ladder (docs/proposals/raati-deliberation.md): what each
// raati level resolves to as per-seat model bindings. Lives in the
// tools package because level 1 rides the swarm tier machinery.

import (
	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/i18n"
)

// ResolveRaatiBindings seats a raati at the given rigor level:
//
//	0 (kaiku)   — every seat on the host binding: charter diversity
//	              only; honest label CORRELATED.
//	1 (kuoro)   — the host provider's tier ladder, strong→weak in seat
//	              order. Deliberately UNCAPPED by the host model: the
//	              maki rule keeps background delegation cheap, but a
//	              level-1 raati is an explicit request for the ladder.
//	2 (käräjät) — the user-configured cross-provider seats
//	              (raati.level2), one exact binding per seat. The only
//	              level with real error decorrelation.
//
// Returns one Binding per seat, or an actionable error when the level
// isn't configured for this host.
func ResolveRaatiBindings(level int, hostProvider, hostModel string, tiers SwarmTierMap, level2 []raati.Binding, seats int) ([]raati.Binding, error) {
	switch level {
	case 0:
		out := make([]raati.Binding, seats)
		for i := range out {
			out[i] = raati.Binding{Provider: hostProvider, Model: hostModel}
		}
		return out, nil
	case 1:
		models, _ := SwarmTierLadder(hostProvider, tiers)
		for _, m := range models {
			if m == "" {
				return nil, i18n.Errorf("rigor level 1 needs a full weak/medium/strong ladder for provider %q — configure swarm_tiers.%s and check it with `terva models tiers`", hostProvider, hostProvider)
			}
		}
		out := make([]raati.Binding, seats)
		for i := range out {
			// Strong→weak in seat order, cycling for larger panels.
			rank := len(models) - 1 - (i % len(models))
			out[i] = raati.Binding{Provider: hostProvider, Model: models[rank]}
		}
		return out, nil
	case 2:
		if len(level2) != seats {
			return nil, i18n.Errorf("rigor level 2 needs exactly %d seat bindings in the user config's raati.level2 (each {\"provider\": …, \"model\": …}); found %d", seats, len(level2))
		}
		out := make([]raati.Binding, seats)
		for i, b := range level2 {
			if b.Provider == "" || b.Model == "" {
				return nil, i18n.Errorf("raati.level2[%d] needs both provider and model", i)
			}
			out[i] = b
		}
		return out, nil
	}
	return nil, i18n.Errorf("unknown rigor level %d (0, 1, or 2)", level)
}

// RaatiLevelName is the ladder nickname for banners and boards.
func RaatiLevelName(level int) string {
	switch level {
	case 1:
		return "kuoro"
	case 2:
		return "käräjät"
	}
	return "kaiku"
}

// HighestRaatiLevel reports the top rigor level this host's config can
// actually seat: 2 with a complete raati.level2, else 1 with a full
// weak/medium/strong ladder for the host provider, else 0. This is the
// "auto" profile level's resolution input — rigor climbs the day the
// config does, without profile edits.
func HighestRaatiLevel(hostProvider string, tiers SwarmTierMap, level2 []raati.Binding, seats int) int {
	if len(level2) == seats {
		complete := true
		for _, b := range level2 {
			if b.Provider == "" || b.Model == "" {
				complete = false
				break
			}
		}
		if complete {
			return 2
		}
	}
	models, _ := SwarmTierLadder(hostProvider, tiers)
	full := len(models) > 0
	for _, m := range models {
		if m == "" {
			full = false
			break
		}
	}
	if full {
		return 1
	}
	return 0
}

// RefuseCorrelatedGate is the shared honesty check for auto-resolved
// profiles: a gate that lands on the correlated level (same weights on
// every seat) refuses rather than quietly gating on theater. Only AUTO
// resolutions refuse — an explicit level-0 gate is the trust root's
// deliberate call. Returns the actionable error, or nil.
func RefuseCorrelatedGate(profileName string, class raati.Class, level int, viaAuto bool) error {
	if !viaAuto || level != 0 || class != raati.ClassGate {
		return nil
	}
	return i18n.Errorf("profile %q: a correlated panel cannot hold a gate — configure swarm_tiers (level 1) or raati.level2 (level 2), raise the profile's auto ceiling, or convene counsel/triage instead", profileName)
}
