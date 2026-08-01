package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// The per-model overrides in models.json — context window, max tokens,
// temperature, base URL — reachable from any frontend rather than only the TUI.
//
// packages/provider already owns what these settings ARE (provider.ScalarParams:
// the key, the label, the kind, the bounds, how to read a default off a model and
// how to write an override onto a UserModel). None of that is restated here. This
// file only carries the descriptor onto the wire and puts the answers back where
// provider says they go, so there is still exactly ONE definition of what a
// context window may be.

// paramHelp explains the settings whose names do not. Keyed by provider's own
// param key, so a renamed key drops its help rather than misattributing it.
var paramHelp = map[string]func() string{
	"name": func() string {
		return i18n.T("What to call this model on screen, in place of its id. Worth setting for local models whose ids run long. Empty uses the id.")
	},
	"desiredContextWindow": func() string {
		return i18n.T("The working window that drives auto-condensing — not the model's ceiling. Keep a large-window model but condense earlier.")
	},
	"contextWindow": func() string {
		return i18n.T("The model's hard ceiling. Leave empty to use what terva knows about this model.")
	},
	"maxTokens": func() string {
		return i18n.T("Cap on a single reply. Empty means the model's own maximum.")
	},
	"baseUrl": func() string {
		return i18n.T("Send this model's requests somewhere else. Changing it rebuilds the client.")
	},
}

// ModelParams describes one model's editable settings: what it would take by
// default, and what models.json currently pins.
func (w *Workspace) ModelParams(_ context.Context, p ctrlproto.ModelParamsParams) (ctrlproto.ModelParamsView, error) {
	m, err := provider.FindModel(p.Provider, p.Model)
	if err != nil {
		return ctrlproto.ModelParamsView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("unknown model %q", p.Model))
	}
	existing, has, err := provider.FindUserModel(config.UserModelsPath(), m.Provider, m.ID)
	if err != nil {
		// A models.json that will not parse is worth saying out loud: the operator's
		// overrides are silently not applying, and a blank form would imply there
		// are none.
		return ctrlproto.ModelParamsView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "read models.json: %v", err)
	}

	out := ctrlproto.ModelParamsView{Provider: m.Provider, Model: m.ID, HasOverride: has}
	for _, sp := range provider.ScalarParams() {
		spec := ctrlproto.ModelParamSpec{
			Key:     sp.Key,
			Label:   sp.Label,
			Kind:    paramKind(sp.Kind),
			Default: sp.Default(m),
			Min:     float64(sp.Min),
			Max:     float64(sp.Max),
		}
		if has {
			spec.Value = sp.Override(existing)
		}
		if h, ok := paramHelp[sp.Key]; ok {
			spec.Help = h()
		}
		out.Params = append(out.Params, spec)
	}
	return out, nil
}

// ModelParamsSet writes the overrides and makes them live.
//
// The whole form arrives, not one field: an empty value CLEARS that override, so
// a partial map could not tell "untouched" from "cleared" and a client that
// omitted a field it had not focused would silently wipe it.
func (w *Workspace) ModelParamsSet(_ context.Context, p ctrlproto.ModelParamsSetParams) error {
	m, err := provider.FindModel(p.Provider, p.Model)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("unknown model %q", p.Model))
	}
	path := config.UserModelsPath()
	existing, _, err := provider.FindUserModel(path, m.Provider, m.ID)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "read models.json: %v", err)
	}
	entry := existing
	entry.ID = m.ID

	// provider validates. Bounds, integer-ness, what a blank means — all of it is
	// decided in ONE place, by the same code the TUI dialog commits through, and a
	// refusal names the setting the operator actually typed into.
	for _, sp := range provider.ScalarParams() {
		v, sent := p.Values[sp.Key]
		if !sent {
			continue
		}
		if err := sp.SetOverride(&entry, strings.TrimSpace(v)); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s: %v", sp.Label, err)
		}
	}

	if err := provider.UpsertUserModel(path, m.Provider, entry); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "save models.json: %v", err)
	}
	w.applyUserModels(m.Provider, m.ID)
	return nil
}

// ModelParamsReset removes the model's models.json entry outright — back to what
// terva ships, not to an entry full of blanks.
func (w *Workspace) ModelParamsReset(_ context.Context, p ctrlproto.ModelParamsParams) error {
	m, err := provider.FindModel(p.Provider, p.Model)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("unknown model %q", p.Model))
	}
	if _, err := provider.RemoveUserModel(config.UserModelsPath(), m.Provider, m.ID); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "save models.json: %v", err)
	}
	w.applyUserModels(m.Provider, m.ID)
	return nil
}

// applyUserModels reloads the override layer and re-resolves any session sitting
// on the edited model.
//
// Both halves matter. Without the reload, the file changed and nothing reads it
// until a restart. Without the re-resolve, a session keeps the client it already
// built — so a new base URL still points at the old machine, and the operator
// watches their edit do nothing.
func (w *Workspace) applyUserModels(prov, modelID string) {
	overrides, _ := provider.LoadUserModelsWithWarnings(config.UserModelsPath())
	provider.SetUserOverrides(overrides)

	w.mu.Lock()
	sessions := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		sessions = append(sessions, s)
	}
	w.mu.Unlock()

	for _, s := range sessions {
		curProv, curModel := s.currentModel()
		if curProv != prov || curModel != modelID {
			continue
		}
		// forceRebuild: a context window can be re-read in place, but a base URL
		// cannot — the client is bound to the endpoint it was built for. Rebuilding
		// unconditionally costs one client and removes a whole class of "the setting
		// saved but nothing changed".
		if err := w.switchModel(s, prov, modelID, true); err != nil {
			w.diag("model settings applied, but the session could not be re-resolved: " + err.Error())
		}
	}
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("models"))
}

// paramKind maps provider's scalar kinds onto the wire's rendering hints. A kind
// provider adds and this does not know becomes text, which is the honest fallback:
// the daemon still parses and still refuses, so the worst case is a plain box
// rather than a wrong widget.
func paramKind(k provider.ScalarKind) string {
	switch k {
	case provider.ScalarInt:
		return "int"
	case provider.ScalarFloat:
		return "float"
	default:
		return "text"
	}
}
