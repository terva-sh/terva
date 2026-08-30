package modes

// Model switching: /model selection, the agent swap that preserves the
// transcript, and the rescue picker that reuses it after provider
// failures.

import (
	"context"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

func (i *Interactive) applyModelSelection(prov, model string) {
	i.swapModel(prov, model, false)
}

// applyRescueModelSelection is like applyModelSelection but tags the swap as a
// rescue retry, so the workspace rebuilds the client rather than swapping the
// model in place — dropping launch-time --api-key / --base-url overrides.
func (i *Interactive) applyRescueModelSelection(prov, model string) {
	i.swapModel(prov, model, true)
}

// swapModel applies a /model selection (or a rescue selection). The workspace
// owns the swap: in-place for the same provider+endpoint, a fresh client
// otherwise (which drops launch-time key/URL overrides — exactly the rescue
// semantics), carrying the transcript across and persisting the session meta so
// resume picks up the same model. rescue=true tags the success message.
func (i *Interactive) swapModel(prov, model string, rescue bool) {
	if model == "" || i.cfg.Carrier == nil {
		return
	}
	i.swapModelCarrier(prov, model, rescue)
}

// persistFavoriteModel saves a favorite toggle for "provider/model". The
// picker has already re-sorted and kept the cursor on the model; this just
// persists the membership change (Ctrl+F in the /model picker).
func (i *Interactive) persistFavoriteModel(prov, model string, on bool) {
	if i.cfg.SetFavoriteModel == nil {
		return
	}
	if err := i.cfg.SetFavoriteModel(prov+"/"+model, on); err != nil {
		i.setStatusErr(i18n.T("favorite: %s", err))
		return
	}
	verb := "favorited"
	if !on {
		verb = "unfavorited"
	}
	i.setStatusOK(model + " " + verb)
}

// persistModelHidden saves a hide toggle for "provider/model". The picker has
// already updated its own set and re-filtered; this persists the change (Ctrl+K
// in the /model picker).
//
// The status line names the reveal token, because a model that has just
// vanished from the list is the moment the user most needs to know how to get
// it back — and the un-hide lives behind a token they have no reason to know.
func (i *Interactive) persistModelHidden(prov, model string, on bool) {
	if i.cfg.SetModelHidden == nil {
		return
	}
	if err := i.cfg.SetModelHidden(prov+"/"+model, on); err != nil {
		i.setStatusErr(i18n.T("hide: %s", err))
		return
	}
	if on {
		i.setStatusOK(i18n.T("%s hidden - type :hidden to show hidden models", model))
		return
	}
	i.setStatusOK(i18n.T("%s shown again", model))
}

// promoteModelDefault persists the current pick as a default in the given
// scope ("project" / "global") and surfaces the outcome on the status line.
// Invoked from the /model picker's Ctrl+D promote prompt, after the switch.
func (i *Interactive) promoteModelDefault(prov, model, scope string) {
	if i.cfg.PromoteModelDefault == nil {
		return
	}
	if err := i.cfg.PromoteModelDefault(prov, model, scope); err != nil {
		i.setStatusErr(i18n.T("set %s default: %s", scope, err))
		return
	}
	where := "global default"
	if scope == "project" {
		where = "project default"
	}
	i.setStatusOK(model + " set as " + where)
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
