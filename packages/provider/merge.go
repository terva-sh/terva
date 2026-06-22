package provider

// discoveryAuthoritative lists providers whose /v1/models response fully
// enumerates their offering, so a successful discovery is the source of truth:
// a baked entry for such a provider that the live endpoint did NOT return is
// stale and dropped (e.g. opencode-go, a subscription tier whose model set
// changes upstream). Providers absent here merge additively — their baked
// catalog stays as an offline fallback. That's required for mixed-protocol
// gateways like opencode, whose Anthropic-flavour models aren't reachable via
// /v1/models and would otherwise be pruned.
//
// The user layer (models.json) is applied AFTER this merge (applyUserOverrides),
// so a user override / custom-endpoint entry always survives even when its baked
// counterpart is pruned here.
var discoveryAuthoritative = map[string]bool{
	"opencode-go": true,
}

// MergeCatalog returns the baked-in catalog overlaid with live entries.
// Precedence per id: live > catalog; speculative entries are promoted
// to non-speculative when a matching live id appears.
//
// Unknown live ids (not in the static catalog) are appended at the end
// with placeholder prices so they still render in the picker. Prices
// can be populated later from a richer catalog source. For
// discovery-authoritative providers (see above), baked entries the live
// endpoint didn't return are dropped as stale.
func MergeCatalog(live []Model) []Model {
	byKey := func(p, id string) string { return p + "/" + id }

	staticIndex := make(map[string]Model, len(Catalog))
	staticOrder := make([]string, 0, len(Catalog))
	for _, m := range Catalog {
		m.Source = "catalog"
		k := byKey(m.Provider, m.ID)
		staticIndex[k] = m
		staticOrder = append(staticOrder, k)
	}

	seenLive := make(map[string]bool, len(live))
	liveProviders := make(map[string]bool)
	for _, m := range live {
		seenLive[byKey(m.Provider, m.ID)] = true
		liveProviders[m.Provider] = true
	}

	// Promote/overwrite from live.
	for _, l := range live {
		k := byKey(l.Provider, l.ID)
		if s, ok := staticIndex[k]; ok {
			// Keep static prices & context window (live endpoint rarely
			// exposes these), but mark as live and non-speculative.
			s.Source = "live"
			s.Speculative = false
			if l.DisplayName != "" {
				s.DisplayName = l.DisplayName
			}
			// Capabilities: discovery fills only keys the curated
			// catalog left absent — catalog explicit beats discovery
			// heuristics, both lose to models.json (applied later).
			s.Caps = mergeCaps(l.Caps, s.Caps)
			staticIndex[k] = s
		} else {
			// New live id we'd never heard of. Best-effort defaults.
			if l.DisplayName == "" {
				l.DisplayName = l.ID
			}
			staticIndex[k] = l
			staticOrder = append(staticOrder, k)
		}
	}

	out := make([]Model, 0, len(staticOrder))
	for _, k := range staticOrder {
		m := staticIndex[k]
		// Drop a baked-only entry (Source still "catalog" -> no live match) for
		// an authoritative provider that WAS discovered this cycle: the live
		// endpoint is the source of truth and didn't list it, so it's stale.
		// A provider that wasn't discovered (offline, no credential) keeps its
		// baked entries as a fallback.
		if m.Source == "catalog" && discoveryAuthoritative[m.Provider] && liveProviders[m.Provider] {
			continue
		}
		out = append(out, m)
	}
	return out
}
