//go:build terva_acp

package acp

import (
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// ---- Phase 4b: model selection (config options) + approval modes ----

// modelMenu is the seeded authenticated-provider model menu used by the
// config-option tests. fake-model is the session's starting model; alt-model
// (same provider) reuses the client; cross-model (a different provider)
// forces a SetClientAndModel switch.
func modelMenu() []ModelOption {
	return []ModelOption{
		{ID: "fake-model", Provider: "fake", DisplayName: "Fake Model"},
		{ID: "alt-model", Provider: "fake", DisplayName: "Alt Model"},
		{ID: "cross-model", Provider: "other", DisplayName: "Cross Model"},
	}
}

// findConfigOption returns the SessionConfigOption with the given id from a
// decoded result's configOptions array, or nil.
func findConfigOption(t *testing.T, configOptions []any, id string) map[string]any {
	t.Helper()
	for _, co := range configOptions {
		m, _ := co.(map[string]any)
		if m["id"] == id {
			return m
		}
	}
	return nil
}

// TestACPSessionNewAdvertisesModelAndModes proves verification (a): session/new
// advertises the `model` config option populated from authenticated providers
// AND the approval-mode menu, with the session's current model/mode as the
// current values.
func TestACPSessionNewAdvertisesModelAndModes(t *testing.T) {
	factory := &fakeFactory{
		client:   &textTurnClient{reply: "hi"},
		tools:    core.Registry{},
		models:   modelMenu(),
		gateMode: core.ApprovalWorkspace, // a real gate so modes are advertised
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()
	_ = sid

	// permSetup already ran session/new; re-run it here to read the result
	// directly (permSetup discards it).
	newRes := h.call(MethodSessionNew, map[string]any{"cwd": testsupport.TempDir(t)})

	// ---- model config option ----
	configOptions, _ := newRes["configOptions"].([]any)
	if len(configOptions) == 0 {
		t.Fatal("session/new advertised no configOptions; want the model selector")
	}
	model := findConfigOption(t, configOptions, ConfigIDModel)
	if model == nil {
		t.Fatalf("no %q config option in %v", ConfigIDModel, configOptions)
	}
	if model["type"] != SessionConfigSelectType {
		t.Errorf("model option type = %v; want %q", model["type"], SessionConfigSelectType)
	}
	if model["currentValue"] != "fake-model" {
		t.Errorf("model currentValue = %v; want fake-model", model["currentValue"])
	}
	opts, _ := model["options"].([]any)
	if len(opts) != 3 {
		t.Errorf("model option has %d values; want 3 (the authenticated menu)", len(opts))
	}
	var sawAlt bool
	for _, o := range opts {
		m, _ := o.(map[string]any)
		if m["value"] == "alt-model" {
			sawAlt = true
		}
	}
	if !sawAlt {
		t.Error("model option missing the alt-model value")
	}

	// ---- approval-mode menu ----
	modes, _ := newRes["modes"].(map[string]any)
	if modes == nil {
		t.Fatal("session/new advertised no modes; want the approval-mode menu")
	}
	if modes["currentModeId"] != string(core.ApprovalWorkspace) {
		t.Errorf("currentModeId = %v; want %q", modes["currentModeId"], core.ApprovalWorkspace)
	}
	available, _ := modes["availableModes"].([]any)
	if len(available) != 5 {
		t.Errorf("availableModes has %d entries; want 5 (plan/ask/auto-edit/workspace/yolo)", len(available))
	}
	wantModes := map[string]bool{"plan": false, "ask": false, "auto-edit": false, "workspace": false, "yolo": false}
	for _, a := range available {
		m, _ := a.(map[string]any)
		if id, ok := m["id"].(string); ok {
			if _, want := wantModes[id]; want {
				wantModes[id] = true
			}
		}
	}
	for id, seen := range wantModes {
		if !seen {
			t.Errorf("availableModes missing %q", id)
		}
	}
}

// TestACPSessionNewNoMenusWhenEmpty proves capability honesty: with no
// authenticated models and no gate, the session result omits both menus rather
// than advertising empty/unusable ones.
func TestACPSessionNewNoMenusWhenEmpty(t *testing.T) {
	factory := &fakeFactory{
		client: &textTurnClient{reply: "hi"},
		tools:  core.Registry{},
		// models empty, no gateMode/askTool -> no gate built
	}
	h, _, teardown := permSetup(t, factory)
	defer teardown()

	newRes := h.call(MethodSessionNew, map[string]any{"cwd": testsupport.TempDir(t)})
	if _, ok := newRes["configOptions"]; ok {
		t.Errorf("configOptions advertised with no authenticated models: %v", newRes["configOptions"])
	}
	if _, ok := newRes["modes"]; ok {
		t.Errorf("modes advertised with no gate: %v", newRes["modes"])
	}
}

// TestACPSetConfigOptionSwitchesModelSameProvider proves verification (b) for a
// same-provider switch: session/set_config_option {model: alt-model} swaps the
// model in place (Reuse), the agent's effective model changes, and a
// config_option_update with the new currentValue is emitted.
func TestACPSetConfigOptionSwitchesModelSameProvider(t *testing.T) {
	factory := &fakeFactory{
		client: &textTurnClient{reply: "hi"},
		tools:  core.Registry{},
		models: modelMenu(),
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	res := h.call(MethodSessionSetConfigOpt, map[string]any{
		"sessionId": sid,
		"configId":  ConfigIDModel,
		"value":     "alt-model",
	})

	// The response echoes the refreshed option list with the new currentValue.
	configOptions, _ := res["configOptions"].([]any)
	model := findConfigOption(t, configOptions, ConfigIDModel)
	if model == nil || model["currentValue"] != "alt-model" {
		t.Fatalf("set_config_option response currentValue = %v; want alt-model", model["currentValue"])
	}

	// The agent's effective model actually changed (not just the menu state).
	ag := factory.lastNewAgent()
	if ag == nil {
		t.Fatal("no new-session agent captured")
	}
	if ag.Model != "alt-model" {
		t.Errorf("agent.Model = %q; want alt-model (effective model must change)", ag.Model)
	}

	// A same-provider switch reuses the client.
	factory.switchMu.Lock()
	reuse := factory.lastSwitch.Reuse
	factory.switchMu.Unlock()
	if !reuse {
		t.Error("same-provider switch did not Reuse the client")
	}

	// config_option_update must have been emitted with the new currentValue.
	assertConfigOptionUpdate(t, h, "alt-model")
}

// TestACPSetConfigOptionSwitchesModelCrossProvider proves verification (b) for
// a cross-provider switch: the host hands back a fresh client (Reuse=false) and
// the acp package SetClientAndModel's it, preserving the session, with the
// effective model + client changed.
func TestACPSetConfigOptionSwitchesModelCrossProvider(t *testing.T) {
	newClient := &textTurnClient{reply: "from the other provider"}
	factory := &fakeFactory{
		client:       &textTurnClient{reply: "hi"},
		tools:        core.Registry{},
		models:       modelMenu(),
		switchClient: newClient,
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	res := h.call(MethodSessionSetConfigOpt, map[string]any{
		"sessionId": sid,
		"configId":  ConfigIDModel,
		"value":     "cross-model",
	})
	configOptions, _ := res["configOptions"].([]any)
	model := findConfigOption(t, configOptions, ConfigIDModel)
	if model == nil || model["currentValue"] != "cross-model" {
		t.Fatalf("currentValue = %v; want cross-model", model["currentValue"])
	}

	ag := factory.lastNewAgent()
	if ag.Model != "cross-model" {
		t.Errorf("agent.Model = %q; want cross-model", ag.Model)
	}
	// SetClientAndModel must have swapped the client too.
	if ag.Client != newClient {
		t.Error("cross-provider switch did not swap the agent's Client (SetClientAndModel)")
	}
	factory.switchMu.Lock()
	reuse := factory.lastSwitch.Reuse
	factory.switchMu.Unlock()
	if reuse {
		t.Error("cross-provider switch should not Reuse the client")
	}

	assertConfigOptionUpdate(t, h, "cross-model")
}

// TestACPSetConfigOptionPersistsModel proves the model change is written to the
// durable session, so a later session/load restores it.
func TestACPSetConfigOptionPersistsModel(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	factory := &fakeFactory{
		client: &textTurnClient{reply: "hi"},
		tools:  core.Registry{},
		models: modelMenu(),
		root:   root,
	}
	h, _, teardown := permSetup(t, factory)
	defer teardown()

	// permSetup's session/new used a throwaway cwd; make a fresh durable one
	// under the root so it persists + is reloadable.
	newRes := h.call(MethodSessionNew, map[string]any{"cwd": cwd})
	sid, _ := newRes["sessionId"].(string)

	h.call(MethodSessionSetConfigOpt, map[string]any{
		"sessionId": sid,
		"configId":  ConfigIDModel,
		"value":     "cross-model",
	})

	// Reopen the durable session directly: its meta must reflect the switch.
	sess, _, err := core.OpenSession(sid)
	if err != nil {
		t.Fatalf("OpenSession(%q): %v", sid, err)
	}
	if sess.Meta.Model != "cross-model" {
		t.Errorf("persisted model = %q; want cross-model (UpdateModel must run)", sess.Meta.Model)
	}
	if sess.Meta.Provider != "other" {
		t.Errorf("persisted provider = %q; want other", sess.Meta.Provider)
	}
}

// TestACPSetConfigOptionUnknownModelInvalidParams proves an unknown model id is
// a -32602 invalid_params error, not a silent no-op.
func TestACPSetConfigOptionUnknownModelInvalidParams(t *testing.T) {
	factory := &fakeFactory{
		client: &textTurnClient{reply: "hi"},
		tools:  core.Registry{},
		models: modelMenu(),
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	assertInvalidParams(t, h, MethodSessionSetConfigOpt, map[string]any{
		"sessionId": sid,
		"configId":  ConfigIDModel,
		"value":     "no-such-model",
	})
}

// TestACPSetConfigOptionUnknownConfigIDInvalidParams proves an unknown configId
// is a -32602 invalid_params error.
func TestACPSetConfigOptionUnknownConfigIDInvalidParams(t *testing.T) {
	factory := &fakeFactory{
		client: &textTurnClient{reply: "hi"},
		tools:  core.Registry{},
		models: modelMenu(),
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	assertInvalidParams(t, h, MethodSessionSetConfigOpt, map[string]any{
		"sessionId": sid,
		"configId":  "bogus",
		"value":     "alt-model",
	})
}

// TestACPSetModeChangesApprovalBehavior proves verification (c): a tool that
// auto-runs under yolo triggers a permission request under ask after
// session/set_mode, and a current_mode_update is emitted.
func TestACPSetModeChangesApprovalBehavior(t *testing.T) {
	tool := &countingTool{name: "do_thing"}
	// Two single-tool prompts: turn 1 (prompt 1) calls the tool, turn 2 ends;
	// turn 3 (prompt 2) calls the tool, turn 4 ends.
	client := &oddToolClient{toolName: "do_thing"}
	factory := &fakeFactory{
		client:   client,
		tools:    core.Registry{"do_thing": tool},
		gateMode: core.ApprovalYolo, // start in yolo: tools auto-run
		models:   modelMenu(),
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	// ---- prompt 1 under yolo: the tool runs WITHOUT a permission request ----
	req1 := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid, "prompt": []map[string]any{{"type": "text", "text": "one"}},
	})
	// A nil permHandler means any permission request would be left unanswered
	// (and the test would hang/timeout). Under yolo none is issued, so the
	// prompt resolves on its own.
	res1 := h.awaitResponse(req1, nil)
	if sr, _ := res1["stopReason"].(string); sr != StopEndTurn {
		t.Fatalf("prompt 1 stopReason = %v; want end_turn", res1["stopReason"])
	}
	if got := atomic.LoadInt32(&tool.runs); got != 1 {
		t.Fatalf("under yolo the tool ran %d times; want 1 (auto-allowed, no prompt)", got)
	}

	// ---- switch to ask ----
	modeRes := h.call(MethodSessionSetMode, map[string]any{
		"sessionId": sid,
		"modeId":    string(core.ApprovalAsk),
	})
	_ = modeRes
	assertCurrentModeUpdate(t, h, string(core.ApprovalAsk))

	// ---- prompt 2 under ask: the SAME tool now triggers a permission ----
	req2 := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid, "prompt": []map[string]any{{"type": "text", "text": "two"}},
	})
	var asked bool
	res2 := h.awaitResponse(req2, func(string) map[string]any {
		asked = true
		return map[string]any{"outcome": PermOutcomeSelected, "optionId": PermAllowOnce}
	})
	if !asked {
		t.Error("under ask the tool did NOT trigger a permission request (mode switch had no effect)")
	}
	if sr, _ := res2["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("prompt 2 stopReason = %v; want end_turn", res2["stopReason"])
	}
	if got := atomic.LoadInt32(&tool.runs); got != 2 {
		t.Errorf("tool ran %d times total; want 2 (allowed once it was approved)", got)
	}
}

// TestACPSetModeUnknownInvalidParams proves an unknown modeId is -32602.
func TestACPSetModeUnknownInvalidParams(t *testing.T) {
	factory := &fakeFactory{
		client:   &textTurnClient{reply: "hi"},
		tools:    core.Registry{},
		gateMode: core.ApprovalWorkspace,
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	assertInvalidParams(t, h, MethodSessionSetMode, map[string]any{
		"sessionId": sid,
		"modeId":    "turbo",
	})
}

// ---- shared assertions ----

// assertConfigOptionUpdate drains the update stream and asserts a
// config_option_update carried the model option with the wanted currentValue.
func assertConfigOptionUpdate(t *testing.T, h *harness, wantValue string) {
	t.Helper()
	for _, u := range h.drainUpdates() {
		upd, _ := u["update"].(map[string]any)
		if upd["sessionUpdate"] != UpdateConfigOption {
			continue
		}
		configOptions, _ := upd["configOptions"].([]any)
		model := findConfigOption(t, configOptions, ConfigIDModel)
		if model != nil && model["currentValue"] == wantValue {
			return
		}
	}
	t.Errorf("no config_option_update with model currentValue %q in the update stream", wantValue)
}

// assertCurrentModeUpdate drains the update stream and asserts a
// current_mode_update carried the wanted modeId.
func assertCurrentModeUpdate(t *testing.T, h *harness, wantMode string) {
	t.Helper()
	for _, u := range h.drainUpdates() {
		upd, _ := u["update"].(map[string]any)
		if upd["sessionUpdate"] == UpdateCurrentMode && upd["currentModeId"] == wantMode {
			return
		}
	}
	t.Errorf("no current_mode_update with currentModeId %q in the update stream", wantMode)
}

// assertInvalidParams sends a request and asserts the response is a -32602
// invalid_params JSON-RPC error.
func assertInvalidParams(t *testing.T, h *harness, method string, params any) {
	t.Helper()
	reqID := h.send(method, params)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out awaiting error response to %s", method)
		}
		f := h.read()
		if rid, ok := f.id.(float64); ok && int(rid) == reqID {
			if f.errObj == nil {
				t.Fatalf("%s did not error; got result %v", method, f.result)
			}
			if code, _ := f.errObj["code"].(float64); int(code) != CodeInvalidParams {
				t.Errorf("%s error code = %v; want %d (invalid_params)", method, f.errObj["code"], CodeInvalidParams)
			}
			return
		}
	}
}

// A model switch has two halves in two scopes. ModelSwitch.Apply moves the
// running agent and the tool instances hanging off it; RecordModelSwap moves
// what the HOST re-resolves from when it later rebuilds the tool set. Apply is
// built per switch by a factory with no session; RecordModelSwap is built per
// session by the composition root. This package is the only thing holding both,
// so it is the only thing that can keep them together — and when they came
// apart, an ACP switch lasted exactly until the next extension reload or
// /trust flip, which re-minted terva_status naming the provider the session had
// switched away from.
//
// Both cases matter, because the endpoint flag is what tells the host whether
// its launch-time key/URL pins still describe the session.
func TestACPSetConfigOptionRecordsTheSwapForTheHost(t *testing.T) {
	for _, tc := range []struct {
		name      string
		target    string
		crossProv bool
		wantProv  string
		wantPins  bool // rebuiltClient: the endpoint moved
	}{
		{"same provider reuses the client", "alt-model", false, "fake", false},
		{"cross provider rebuilds it", "cross-model", true, "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := &fakeFactory{
				client: &textTurnClient{reply: "hi"},
				tools:  core.Registry{},
				models: modelMenu(),
			}
			if tc.crossProv {
				factory.switchClient = &textTurnClient{reply: "switched"}
			}
			h, sid, teardown := permSetup(t, factory)
			defer teardown()

			h.call(MethodSessionSetConfigOpt, map[string]any{
				"sessionId": sid,
				"configId":  ConfigIDModel,
				"value":     tc.target,
			})

			got := factory.swapsRecorded()
			if len(got) != 1 {
				t.Fatalf("RecordModelSwap fired %d times, want exactly 1 — a switch the host "+
					"never hears about survives only until its next tool-set rebuild", len(got))
			}
			if got[0].model != tc.target {
				t.Errorf("recorded model = %q, want %q", got[0].model, tc.target)
			}
			if got[0].provider != tc.wantProv {
				t.Errorf("recorded provider = %q, want %q", got[0].provider, tc.wantProv)
			}
			if got[0].rebuiltClient != tc.wantPins {
				t.Errorf("recorded rebuiltClient = %v, want %v — this is how the host learns "+
					"whether its launch-time endpoint pins still apply", got[0].rebuiltClient, tc.wantPins)
			}
		})
	}
}

// A host that re-resolves nothing wires no hook, and that must stay a supported
// shape rather than a panic on the switch path.
func TestACPSetConfigOptionToleratesNoRecordHook(t *testing.T) {
	factory := &fakeFactory{
		client:       &textTurnClient{reply: "hi"},
		tools:        core.Registry{},
		models:       modelMenu(),
		noRecordSwap: true,
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	res := h.call(MethodSessionSetConfigOpt, map[string]any{
		"sessionId": sid,
		"configId":  ConfigIDModel,
		"value":     "alt-model",
	})
	configOptions, _ := res["configOptions"].([]any)
	if model := findConfigOption(t, configOptions, ConfigIDModel); model == nil || model["currentValue"] != "alt-model" {
		t.Fatalf("the switch did not land without a record hook: %v", res)
	}
}
