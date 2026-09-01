package modes

// Host side of the /model dialog's tier stage: fetching a provider's swarm
// ladder and writing a rung back. The dialog does no I/O of its own — the
// ladder lives in config and is resolved against the catalog by the daemon.

import (
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

// openModelTiers fetches a provider's ladder and hands it to the dialog.
func (i *Interactive) openModelTiers(prov string) {
	if i.cfg.ModelTiers == nil {
		i.setStatusErr(i18n.T("tiers: not available from this session"))
		i.invalidate()
		return
	}
	view, err := i.cfg.ModelTiers(prov)
	if err != nil {
		i.setStatusErr(i18n.T("tiers: %s", err))
		i.invalidate()
		return
	}
	i.modelDialog.ShowTiers(view)
	i.invalidate()
}

// setModelTier pins a rung, then re-reads the ladder so the screen shows what
// the change actually resolved to rather than what was asked for. The two can
// differ — a rung pinned to a level only, on a model whose ladder collapses
// that rung onto another, comes back as the rung it collapsed to.
func (i *Interactive) setModelTier(prov, rung, model, reasoning string) {
	if i.cfg.SetModelTier == nil {
		i.setStatusErr(i18n.T("tiers: not available from this session"))
		i.invalidate()
		return
	}
	if err := i.cfg.SetModelTier(prov, rung, model, reasoning); err != nil {
		i.setStatusErr(i18n.T("tiers: %s", err))
		i.invalidate()
		return
	}
	i.refreshModelTiers(prov)
}

// resetModelTier drops a rung's pin and re-reads, for the same reason.
func (i *Interactive) resetModelTier(prov, rung string) {
	if i.cfg.ResetModelTier == nil {
		i.setStatusErr(i18n.T("tiers: not available from this session"))
		i.invalidate()
		return
	}
	if err := i.cfg.ResetModelTier(prov, rung); err != nil {
		i.setStatusErr(i18n.T("tiers: %s", err))
		i.invalidate()
		return
	}
	i.refreshModelTiers(prov)
}

// refreshModelTiers re-reads the ladder into the dialog, but only while the
// stage is still up: a write whose refresh raced the user pressing esc must not
// drag them back into a screen they just left.
func (i *Interactive) refreshModelTiers(prov string) {
	if i.cfg.ModelTiers == nil {
		i.invalidate()
		return
	}
	view, err := i.cfg.ModelTiers(prov)
	if err == nil {
		// The summary is updated whether or not the stage is still up. Backing
		// out of the ladder is the ordinary way to see the glyph column, so a
		// refresh that only ran while the ladder was on screen would leave the
		// row a user just edited showing its old state.
		i.modelDialog.UpdateTierSummary(prov, view)
	}
	if err == nil && i.modelDialog.TierStageActive() {
		i.modelDialog.ShowTiers(view)
	}
	i.invalidate()
}

// loadTierSummaries fills the provider list's glyph column, one fetch per
// logged-in provider.
//
// One call per provider rather than one call for all of them: there is no
// all-providers verb, and adding one to save a handful of round trips would put
// a second definition of "a ladder" on the wire. In attach mode these ARE round
// trips to the daemon, on a keypress — which is only safe because ModelTiers
// reads config and the catalog and touches no workspace state, so it takes no
// lock and cannot queue behind a running turn. A verb that did would have to be
// fetched off this path instead.
//
// A provider whose ladder cannot be read is skipped rather than failing the
// open — the column is an aid, and a picker that refused to appear because a
// summary was unavailable would be a worse trade than a missing glyph.
func (i *Interactive) loadTierSummaries(providers []string) {
	if i.cfg.ModelTiers == nil || len(providers) == 0 {
		return
	}
	views := make(map[string]ctrlproto.ModelTiersView, len(providers))
	for _, prov := range providers {
		if _, seen := views[prov]; seen {
			continue
		}
		if v, err := i.cfg.ModelTiers(prov); err == nil {
			views[prov] = v
		}
	}
	i.modelDialog.SetTierSummaries(views)
}
