//go:build terva_acp

package acp

import (
	"encoding/json"

	"terva.sh/terva/packages/core"
)

// Phase 4b: model selection (config options) + approval mode (session modes).
//
// ACP has no session/set_model — a model is a `select` config option
// (configId "model") whose allowed values are the authenticated providers'
// models. Approval mode is a SEPARATE first-class method: session/set_mode
// over a SessionModeState advertised on the session result. This file builds
// both menus and handles both switches (§4/§13).

// ---- model config option ----

// sessionConfigOptions builds the `configOptions` array for a session result.
// The only option this pass is the model selector: a select option whose
// values are the authenticated-provider models the factory reports (plan §14
// "authenticated only"), with the session's current model as currentValue.
//
// When the host advertises no authenticated models (a degenerate setup), we
// return nil so the session result omits configOptions rather than advertising
// an empty, unusable menu (capability honesty).
func (s *agentServer) sessionConfigOptions(sess *session) []SessionConfigOption {
	opts := s.factory.ModelOptions()
	if len(opts) == 0 {
		return nil
	}
	_, current := sess.currentModel()
	values := make([]SessionConfigSelectOption, 0, len(opts))
	for _, o := range opts {
		label := o.DisplayName
		if label == "" {
			label = o.ID
		}
		values = append(values, SessionConfigSelectOption{
			Value:       o.ID,
			Name:        label,
			Description: o.Provider,
		})
	}
	return []SessionConfigOption{{
		ID:           ConfigIDModel,
		Name:         "Model",
		Type:         SessionConfigSelectType,
		Description:  "The model terva runs for this session.",
		Category:     ConfigCategoryModel,
		CurrentValue: current,
		Options:      values,
	}}
}

// handleSetConfigOption handles session/set_config_option {sessionId, configId,
// value}. The only config this pass is "model": it switches the session's
// agent to the selected model (cross-provider preserves the transcript via
// SetClientAndModel; same provider+endpoint swaps in place via SetModel),
// persists the change to the durable session, emits a config_option_update
// with the refreshed option list, and echoes that list in the response.
//
// An unknown configId, an unknown/unauthenticated model, or a same-provider
// model that routes to a different endpoint is a -32602 invalid_params error
// (never a silent no-op — §4b capability honesty).
func (s *agentServer) handleSetConfigOption(params json.RawMessage) (any, error) {
	var p SetSessionConfigOptionRequest
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errInvalidParams(err.Error())
	}
	if p.SessionID == "" {
		return nil, errInvalidParams("sessionId is required")
	}
	if p.ConfigID != ConfigIDModel {
		return nil, errInvalidParams("unknown configId: " + p.ConfigID)
	}
	if p.Value == "" {
		return nil, errInvalidParams("value is required")
	}

	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &rpcError{Code: CodeResourceNotFound, Message: "unknown session: " + p.SessionID}
	}

	curProv, curModel := sess.currentModel()
	sw, err := s.factory.SwitchModel(curProv, curModel, p.Value)
	if err != nil {
		// Unknown model / unauthenticated provider / cross-endpoint same
		// provider — all map to invalid_params: the editor sent a value we
		// can't honor for this session.
		return nil, errInvalidParams(err.Error())
	}

	// Apply the switch to the live agent through the host's swap closure, which
	// is the same event the daemon, bot mode and the resume path go through
	// (build.ApplyModelSwap) — so the four cannot drift on what a swap
	// consists of: the usage carry-over, the client install, the terva_status
	// re-bind, and re-pointing every host-routed dispatch tool (§10/§14).
	//
	// This used to be spelled out here, in a copy whose own comments named the
	// file it was copied from.
	if sw.Apply == nil {
		// The host built a switch it cannot apply. Say so rather than
		// recording the new model and telling the editor it took effect.
		return nil, errInternal("model switch is not applicable: the host supplied no applier")
	}
	sw.Apply(sess.agent)
	sess.setModel(sw.Provider, sw.Model)
	// The other half of the same event, and it must not drift from the line
	// above: Apply moved the agent and the tool instances hanging off it, this
	// moves what the host RE-RESOLVES from. Skip it and the swap lasts until the
	// next tool-set rebuild, which re-mints those instances from the model the
	// session was built on. A nil hook is a host that re-resolves nothing.
	if sess.recordSwap != nil {
		sess.recordSwap(sw.Provider, sw.Model, sw.Client != nil)
	}

	// Persist the model change so a later session/load restores it (§4b). The
	// durable session keeps the most recent meta entry.
	if sess.durable != nil {
		_ = sess.durable.UpdateModel(sw.Provider, sw.Model)
	}

	options := s.sessionConfigOptions(sess)
	// Notify the editor of the new menu state (config_option_update). The
	// refreshed list carries the new currentValue.
	sess.emit(map[string]any{
		"sessionUpdate": UpdateConfigOption,
		"configOptions": options,
	})

	return SetSessionConfigOptionResult{ConfigOptions: options}, nil
}

// ---- approval-mode session modes ----

// approvalModeMenu is the ordered list of terva approval modes surfaced as ACP
// session modes, with human labels and one-line descriptions. The id is the
// ApprovalMode string so session/set_mode round-trips straight through
// core.ParseApprovalMode.
var approvalModeMenu = []SessionMode{
	{ID: string(core.ApprovalPlan), Name: "Plan", Description: "Read-only: mutating tools are refused so the model proposes a plan."},
	{ID: string(core.ApprovalAsk), Name: "Ask", Description: "Prompt before every tool call, reads included."},
	{ID: string(core.ApprovalAutoEdit), Name: "Auto-edit", Description: "Auto-allow reads and file edits; prompt for everything else."},
	{ID: string(core.ApprovalWorkspace), Name: "Workspace", Description: "Trust first-party tools and reads; prompt for foreign side effects."},
	{ID: string(core.ApprovalYolo), Name: "Yolo", Description: "Auto-allow everything."},
}

// sessionModeState builds the SessionModeState for a session result: the full
// approval-mode menu plus the session's current mode. Returns nil when the
// session has no gate (a pure-yolo session can't switch modes — there is
// nothing to re-mode — so we don't advertise a menu we can't honor).
func sessionModeState(sess *session) *SessionModeState {
	if sess.gate == nil {
		return nil
	}
	return &SessionModeState{
		CurrentModeID:  sess.currentMode(),
		AvailableModes: approvalModeMenu,
	}
}

// handleSetMode handles session/set_mode {sessionId, modeId}. It validates the
// mode, switches the session's ConfirmGate to it (copy-on-write via
// gate.SetMode — race-safe, and crucially keeps the ACP confirmer wired as the
// inner Confirmer, so an "ask" mode still drives session/request_permission),
// records the new mode, and emits a current_mode_update notification.
//
// An unknown modeId is -32602 invalid_params; a session with no gate (pure
// yolo) can't re-mode, which is also invalid_params (§4b capability honesty —
// we advertise no modes for such a session, so a well-behaved client never
// asks).
func (s *agentServer) handleSetMode(params json.RawMessage) (any, error) {
	var p SetSessionModeRequest
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errInvalidParams(err.Error())
	}
	if p.SessionID == "" {
		return nil, errInvalidParams("sessionId is required")
	}

	mode, err := core.ParseApprovalMode(p.ModeID)
	if err != nil {
		return nil, errInvalidParams(err.Error())
	}

	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &rpcError{Code: CodeResourceNotFound, Message: "unknown session: " + p.SessionID}
	}
	if sess.gate == nil {
		return nil, errInvalidParams("this session has no approval gate; mode switching is unavailable")
	}

	// SetMode swaps the policy's Mode by copy-on-write under the gate lock, so
	// the next tool call is gated by the new mode. The inner Confirmer (the
	// ACP confirmer) is left intact, so an "ask"/"plan"/... mode still routes
	// to session/request_permission. No agent rebuild, so the transcript and
	// the per-session permission memory survive.
	sess.gate.SetMode(mode)
	sess.setMode(string(mode))

	// Notify the editor of the new mode (current_mode_update).
	sess.emit(map[string]any{
		"sessionUpdate": UpdateCurrentMode,
		"currentModeId": string(mode),
	})

	return SetSessionModeResult{}, nil
}
