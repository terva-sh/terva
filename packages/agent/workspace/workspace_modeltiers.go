package workspace

import (
	"context"
	"slices"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// The swarm tier ladder on the wire. packages/agent/tools owns what a ladder IS
// (the family rules, the override precedence, what each rung resolves to); this
// file only carries that answer out and puts a pin back where config says it
// goes, so there is still one definition of the ladder.
//
// The RESOLVED pick is the payload that matters. Everything about this ladder
// was previously invisible unless you ran one CLI subcommand, which is how
// google's medium and strong rungs sat on image-generation models with every
// guard passing.

// ModelTiers describes one provider's ladder as it stands today.
func (w *Workspace) ModelTiers(_ context.Context, p ctrlproto.ModelTiersParams) (ctrlproto.ModelTiersView, error) {
	prov := strings.TrimSpace(p.Provider)
	if prov == "" {
		return ctrlproto.ModelTiersView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("provider is required"))
	}
	cfg, _ := config.LoadConfig()
	_, has := cfg.SwarmTiers[prov]
	names, picks, sources := tools.SwarmTierTable(prov, build.SwarmTierMap(cfg.SwarmTiers))

	tc := cfg.SwarmTiers[prov]
	out := ctrlproto.ModelTiersView{Provider: prov, HasOverride: has}
	for rank, rung := range names {
		row := ctrlproto.ModelTierRung{
			Rung:      rung,
			Model:     picks[rank].Model,
			Pinned:    strings.TrimSpace(tierRungOf(tc, rung).Model),
			Reasoning: picks[rank].Reasoning,
			Source:    sources[rank],
		}
		// The label is looked up here rather than by each client: a rung that
		// resolves to a model the catalog no longer has should still render as
		// its id instead of vanishing.
		if row.Model != "" {
			if m, err := provider.FindModel(prov, row.Model); err == nil {
				row.Label = m.Label()
			}
		}
		out.Rungs = append(out.Rungs, row)
	}
	return out, nil
}

// ModelTiersSet pins one rung.
func (w *Workspace) ModelTiersSet(_ context.Context, p ctrlproto.ModelTiersSetParams) error {
	prov, rung, err := tierTarget(p.Provider, p.Rung)
	if err != nil {
		return err
	}
	pin := config.TierRung{Model: strings.TrimSpace(p.Model)}
	if pin.Model != "" {
		// Refuse an id this provider does not have. A rung that names a
		// missing model does not fail here — it silently resolves to nothing
		// and the sub-agent quietly inherits the host, which is the failure
		// this whole surface exists to stop being invisible.
		if _, err := provider.FindModel(prov, pin.Model); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("unknown model %q", p.Model))
		}
	}
	// Same vocabulary, same validator, as a session's own thinking level: a
	// level learned from one surface must not be refused by another.
	if pin.Reasoning, err = normalizeSessionReasoning(p.Reasoning); err != nil {
		return err
	}
	if pin.Model == "" && pin.Reasoning == "" {
		// Nothing pinned is a reset, and saying so beats writing an empty rung
		// that reads as a pin in the file.
		return w.ModelTiersReset(context.Background(), ctrlproto.ModelTiersResetParams{Provider: prov, Rung: rung})
	}

	if err := config.MutateConfig(func(c *config.Config) {
		if c.SwarmTiers == nil {
			c.SwarmTiers = map[string]config.TierConfig{}
		}
		tc := c.SwarmTiers[prov]
		setTierRung(&tc, rung, pin)
		c.SwarmTiers[prov] = tc
	}); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
	}
	w.applySwarmTiers()
	return nil
}

// ModelTiersReset drops one rung's pin, or the provider's whole entry when Rung
// is empty.
func (w *Workspace) ModelTiersReset(_ context.Context, p ctrlproto.ModelTiersResetParams) error {
	prov := strings.TrimSpace(p.Provider)
	if prov == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("provider is required"))
	}
	rung := strings.ToLower(strings.TrimSpace(p.Rung))
	if rung != "" && !slices.Contains(tools.SwarmTierNames(), rung) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown tier %q (weak|medium|strong|cheap)", p.Rung))
	}
	if err := config.MutateConfig(func(c *config.Config) {
		if c.SwarmTiers == nil {
			return
		}
		if rung == "" {
			delete(c.SwarmTiers, prov)
			return
		}
		tc, ok := c.SwarmTiers[prov]
		if !ok {
			return
		}
		setTierRung(&tc, rung, config.TierRung{})
		// An entry with no rungs left is dropped rather than kept as an empty
		// object: HasOverride answers "would a reset do anything", and an empty
		// husk would answer yes forever.
		if tc == (config.TierConfig{}) {
			delete(c.SwarmTiers, prov)
		} else {
			c.SwarmTiers[prov] = tc
		}
	}); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
	}
	w.applySwarmTiers()
	return nil
}

// tierTarget validates and normalizes a provider + rung pair.
func tierTarget(providerID, rung string) (string, string, error) {
	prov := strings.TrimSpace(providerID)
	if prov == "" {
		return "", "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("provider is required"))
	}
	r := strings.ToLower(strings.TrimSpace(rung))
	if !slices.Contains(tools.SwarmTierNames(), r) {
		return "", "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown tier %q (weak|medium|strong|cheap)", rung))
	}
	return prov, r, nil
}

// tierRungOf reads one named rung, the counterpart to setTierRung and here for
// the same reason: the struct has a field per rung, so the name has to be
// turned back into a field somewhere, once.
func tierRungOf(tc config.TierConfig, rung string) config.TierRung {
	return tc.Rungs()[rung]
}

// setTierRung writes one named rung of a TierConfig. The struct has a field per
// rung rather than a map, so the name has to be turned back into a field
// somewhere; doing it once here keeps set and reset from disagreeing.
func setTierRung(tc *config.TierConfig, rung string, v config.TierRung) {
	switch rung {
	case "weak":
		tc.Weak = v
	case "medium":
		tc.Medium = v
	case "strong":
		tc.Strong = v
	case tools.TierCheap:
		tc.Cheap = v
	}
}

// applySwarmTiers makes a ladder change live. The tier map is read when a
// session's tools are built (workspace_session.go), so a running session holds
// the ladder it started with until its tools are rebuilt — the same treatment
// auto-swarm gets, and for the same reason: without it the file changed and the
// next spawn still goes to the old model.
func (w *Workspace) applySwarmTiers() {
	w.rebuildAllSessions("swarm-tiers")
}
