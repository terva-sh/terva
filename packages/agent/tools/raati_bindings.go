package tools

// The rigor ladder (docs/proposals/raati-deliberation.md): what each
// raati level resolves to as per-seat model bindings. Lives in the
// tools package because level 1 rides the swarm tier machinery.

import (
	"sort"

	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/i18n"
)

// SpareHostLadder picks the provider whose tier ladder a level-1 raati
// rides: the host's own, unless raati.spare_host asks the panel to stay
// off the session's account and the config offers an alternative. Panel
// units on the convening session's provider compete for the same
// account-level prompt cache — five same-account units measured alongside
// one session evicted its 200K cached prefix, which the session then
// re-read at full price.
//
// Candidates are only providers the user has explicitly configured
// (swarm_tiers overrides and raati.level2 seats — the ones credentials
// are intended for), sorted for determinism; the first with a full
// ladder wins. No candidate means the host's own ladder: sparing
// degrades, it never refuses a panel the unspared config would seat.
func SpareHostLadder(hostProvider string, spare bool, tiers SwarmTierMap, level2 []raati.Binding) string {
	if !spare {
		return hostProvider
	}
	seen := map[string]bool{hostProvider: true}
	var cands []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			cands = append(cands, id)
		}
	}
	for id := range tiers {
		add(id)
	}
	for _, b := range level2 {
		add(b.Provider)
	}
	sort.Strings(cands)
	for _, id := range cands {
		picks, _ := SwarmTierLadder(id, tiers)
		full := true
		for _, p := range picks {
			if p.Model == "" {
				full = false
				break
			}
		}
		if full {
			return id
		}
	}
	return hostProvider
}

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
// ladderProvider names whose tier ladder level 1 rides — normally the
// host's own; SpareHostLadder may move it off the session's account.
// Level 0 always seats the HOST binding regardless: its documented
// semantics are "this session's model", and sparing must not quietly
// change what an explicit level 0 means.
//
// Returns one Binding per seat, or an actionable error when the level
// isn't configured for this host.
func ResolveRaatiBindings(level int, hostProvider, hostModel, ladderProvider string, tiers SwarmTierMap, level2 []raati.Binding, seats int) ([]raati.Binding, error) {
	switch level {
	case 0:
		out := make([]raati.Binding, seats)
		for i := range out {
			out[i] = raati.Binding{Provider: hostProvider, Model: hostModel}
		}
		return out, nil
	case 1:
		if ladderProvider == "" {
			ladderProvider = hostProvider
		}
		picks, _ := SwarmTierLadder(ladderProvider, tiers)
		for _, p := range picks {
			if p.Model == "" {
				return nil, i18n.Errorf("rigor level 1 needs a full weak/medium/strong ladder for provider %q — configure swarm_tiers.%s and check it with `terva models tiers`, or drop the explicit level and let a profile's auto pick seat the highest rigor this config supports", ladderProvider, ladderProvider)
			}
		}
		out := make([]raati.Binding, seats)
		for i := range out {
			// Strong→weak in seat order, cycling for larger panels.
			rank := len(picks) - 1 - (i % len(picks))
			out[i] = raati.Binding{Provider: ladderProvider, Model: picks[rank].Model, Reasoning: picks[rank].Reasoning}
		}
		return out, nil
	case 2:
		if len(level2) != seats {
			return nil, i18n.Errorf("rigor level 2 needs exactly %d seat bindings in the user config's raati.level2 (each {\"provider\": …, \"model\": …}); found %d — or drop the explicit level and let a profile's auto pick seat the highest rigor this config supports", seats, len(level2))
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
	picks, _ := SwarmTierLadder(hostProvider, tiers)
	full := len(picks) > 0
	for _, p := range picks {
		if p.Model == "" {
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
// profiles: a gate whose seats all carry the same weights refuses rather
// than quietly gating on theater. Only AUTO resolutions refuse — an
// explicit correlated gate is the trust root's deliberate call. Returns
// the actionable error, or nil.
//
// It reads the RESOLVED POOL rather than the level number, because the
// level number stopped implying correlation the day a rung could name a
// thinking effort. Level 1 on a provider whose ladder is one model at three
// efforts is a real advisory panel — thinking off and thinking hard are
// materially different judges — but it is not three independent ones, and
// a gate is exactly the thing that must not be told otherwise. The two
// cases get different messages because they have different fixes.
func RefuseCorrelatedGate(profileName string, class raati.Class, pool []raati.Binding, viaAuto bool) error {
	if !viaAuto || class != raati.ClassGate || len(pool) < 2 || !raati.SameWeights(pool) {
		return nil
	}
	if raati.SameEffort(pool) {
		return i18n.Errorf("profile %q: a correlated panel cannot hold a gate — configure swarm_tiers (level 1) or raati.level2 (level 2), raise the profile's auto ceiling, or convene counsel/triage instead", profileName)
	}
	return i18n.Errorf("profile %q: these seats are one model at %d thinking levels — a real advisory panel, but not %d independent judges, so it cannot hold a gate. Give %s a cross-model ladder in swarm_tiers, configure raati.level2, or convene counsel instead", profileName, len(pool), len(pool), pool[0].Provider)
}
