package modes

// Model switching: /model selection, the agent swap that preserves the
// transcript, and the rescue picker that reuses it after provider
// failures.

import (
	"context"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func (i *Interactive) applyModelSelection(prov, model string) {
	i.swapModel(prov, model, i.cfg.BuildAgentFor, false)
}

// applyRescueModelSelection is like applyModelSelection but routes
// through BuildAgentForRescue so launch-time --api-key / --base-url
// overrides are dropped before the new agent is built. Falls back to
// the regular builder when the host doesn't wire a rescue builder.
func (i *Interactive) applyRescueModelSelection(prov, model string) {
	builder := i.cfg.BuildAgentForRescue
	if builder == nil {
		builder = i.cfg.BuildAgentFor
	}
	i.swapModel(prov, model, builder, true)
}

// swapModel applies a /model selection (or a rescue selection) using
// the supplied builder. rescue=true tags the success message so the
// user can see that launch-time overrides were ignored.
func (i *Interactive) swapModel(prov, model string, builder func(string, string) (*core.Agent, string, string, error), rescue bool) {
	if model == "" {
		return
	}
	m, err := provider.FindModel(prov, model)
	if err != nil {
		i.mu.Lock()
		i.statusErr = err.Error()
		i.mu.Unlock()
		return
	}
	// Same provider AND not a rescue retry: just swap the model on
	// the existing agent — no rebuild needed because the underlying
	// client is reusable. Rescue retries always rebuild so a stale
	// auth header / base URL can't carry over.
	//
	// "Reusable" only holds when the resolved endpoint doesn't change.
	// A per-model models.json baseUrl can route two models of the same
	// provider to different backends (one on a gateway, another local).
	// The provider client captures its base URL immutably at construction
	// and the terva_status tool caches it at build time, so mutating Model
	// in place would keep firing requests at — and reporting — the
	// previous model's endpoint. When the base URL differs, fall through
	// to a full rebuild (which re-runs Resolve and reconstructs both the
	// client and the status tool). Without a builder we can't rebuild, so
	// keep the in-place swap as a fallback — no worse than before.
	if !rescue && i.turns.HasAgent() && m.Provider == i.cfg.Provider {
		cur, curErr := provider.FindModel(i.cfg.Provider, i.cfg.Model)
		endpointChanged := curErr != nil || cur.BaseURL != m.BaseURL
		if !endpointChanged || builder == nil {
			i.turns.Agent().SetModel(m.ID)
			i.mu.Lock()
			i.cfg.Model = m.ID
			i.statusOK = "model: " + m.ID
			i.statusErr = ""
			i.mu.Unlock()
			if i.cfg.PersistModel != nil {
				i.cfg.PersistModel(i.cfg.Provider, m.ID)
			}
			return
		}
	}
	if builder == nil {
		i.mu.Lock()
		i.statusErr = "cannot switch provider: no builder configured"
		i.mu.Unlock()
		return
	}
	// Snapshot the current transcript and cumulative usage BEFORE we
	// build the replacement agent so we can hand them off. Without
	// this the user perceives the entire session as wiped on a
	// cross-provider /model swap.
	var carryMsgs []provider.Message
	var carryCost provider.Usage
	if cur := i.turns.Agent(); cur != nil {
		carryMsgs = cur.Messages()
		carryCost = cur.Cost()
	}

	ag, p, md, err := builder(m.Provider, m.ID)
	if err != nil {
		i.mu.Lock()
		i.statusErr = err.Error()
		i.mu.Unlock()
		return
	}

	// Replay the transcript and seed the cost on the freshly-built
	// agent. Messages travel cleanly between providers because they
	// use the same provider.Message shape; tool-call ids are local
	// to a turn so cross-provider continuation never confuses the
	// new model (it just sees the assistant's reply, no orphan
	// tool_use blocks because /model swaps are gated to idle state).
	if len(carryMsgs) > 0 {
		ag.SetMessages(carryMsgs)
	}
	ag.SeedCost(carryCost)

	i.turns.SetAgent(ag)
	i.mu.Lock()
	i.cfg.Provider = p
	i.cfg.Model = md
	if rescue {
		i.statusOK = "rescue retry: switched to " + p + " / " + md + " (ignored --api-key / --base-url)"
	} else {
		i.statusOK = "switched to " + p + " / " + md
	}
	i.statusErr = ""
	// Render cache keys are width+content based, so the new agent's
	// identical messages will reuse the existing entries. Nothing
	// to invalidate.
	i.mu.Unlock()
	// The new agent was built off the base tool registry, so any
	// dynamically-registered tools (telegram_send_*) need to be
	// reattached. applyChatTools is a no-op when the bridge is
	// idle so the cross-provider path still works on a vanilla setup.
	i.applyChatTools(i.chatBridge != nil && i.chatBridge.Active())
	if i.cfg.PersistModel != nil {
		i.cfg.PersistModel(p, md)
	}
}

// openRescueDialog surfaces the rescue model picker after a recoverable
// provider failure. The pending prompt + images are stashed on the
// Interactive so a later applyRescueSelection can re-run the same turn
// against the freshly-picked model. activeProvider/failedProvider are
// usually the same, but some clients embed different prefixes in their
// errors than the configured provider id, so we accept both.
func (i *Interactive) openRescueDialog(activeProvider, failedProvider, failedModel, reason, prompt string, images []provider.ImageBlock) {
	if i.rescueDialog == nil {
		return
	}
	loggedIn := []string{}
	if i.cfg.LoggedInProviders != nil {
		loggedIn = i.cfg.LoggedInProviders()
	}
	fprov := failedProvider
	if fprov == "" {
		fprov = activeProvider
	}
	i.mu.Lock()
	i.pendingRescuePrompt = prompt
	i.pendingRescueImages = images
	i.mu.Unlock()
	i.rescueDialog.Open(failedModel, loggedIn, fprov, failedModel, reason, prompt)
	i.invalidate()
}

// applyRescueSelection switches model (cross-provider if needed) and
// re-runs the same prompt+images that just failed. Mirrors
// applyModelSelection's transcript-carry logic so the user keeps full
// session continuity across the swap.
func (i *Interactive) applyRescueSelection(prov, model, prompt string) {
	if model == "" {
		return
	}
	i.applyRescueModelSelection(prov, model)
	i.mu.Lock()
	images := i.pendingRescueImages
	if prompt == "" {
		prompt = i.pendingRescuePrompt
	}
	i.pendingRescuePrompt = ""
	i.pendingRescueImages = nil
	i.mu.Unlock()
	parent := i.runCtx
	if parent == nil {
		parent = context.Background()
	}
	i.startTurnWithImages(parent, prompt, images)
}
