package modes

import (
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

type fakeSettingsStore struct{}

func (f *fakeSettingsStore) SetInlineImages(bool) error         { return nil }
func (f *fakeSettingsStore) SetAutoSwarm(bool) error            { return nil }
func (f *fakeSettingsStore) SetRecursiveFileSuggest(bool) error { return nil }
func (f *fakeSettingsStore) SetRespectGitignore(bool) error     { return nil }
func (f *fakeSettingsStore) SetReasoning(string) error          { return nil }
func (f *fakeSettingsStore) SetTheme(string) error              { return nil }

func newApprovalTestInteractive(gate *core.ConfirmGate, store SettingsStore, rebuilt *core.ApprovalMode) *Interactive {
	return &Interactive{
		turns: newTurnEngine(),
		dirty: make(chan struct{}, 1),
		cfg: InteractiveConfig{
			ConfirmGate:   gate,
			SettingsStore: store,
			SetApprovalMode: func(m core.ApprovalMode) core.Registry {
				gate.SetMode(m)
				*rebuilt = m
				return core.Registry{}
			},
		},
	}
}

func TestApprovalSettingItemReflectsGateMode(t *testing.T) {
	gate := core.NewPolicyGate(&core.PermissionPolicy{Mode: core.ApprovalAutoEdit, ReadOnly: core.NewReadOnlySet("read"), EditTools: map[string]bool{}}, nil)
	var rebuilt core.ApprovalMode
	i := newApprovalTestInteractive(gate, &fakeSettingsStore{}, &rebuilt)

	item, ok := i.approvalSettingItem()
	if !ok {
		t.Fatal("want an approval item when gate + callback are present")
	}
	if item.key != "approval_mode" {
		t.Fatalf("item key = %q", item.key)
	}
	if got := item.options[item.choice].value; got != "auto-edit" {
		t.Errorf("picker choice = %q, want auto-edit (the gate's mode)", got)
	}
}

func TestApprovalSettingItemAbsentWithoutWiring(t *testing.T) {
	// No gate, or no rebuild callback → no picker (embedders/tests).
	i := &Interactive{dirty: make(chan struct{}, 1), turns: newTurnEngine()}
	if _, ok := i.approvalSettingItem(); ok {
		t.Error("no gate → no approval picker")
	}
	gate := core.NewPolicyGate(&core.PermissionPolicy{Mode: core.ApprovalYolo, ReadOnly: core.NewReadOnlySet(), EditTools: map[string]bool{}}, nil)
	i2 := &Interactive{dirty: make(chan struct{}, 1), turns: newTurnEngine(), cfg: InteractiveConfig{ConfirmGate: gate}}
	if _, ok := i2.approvalSettingItem(); ok {
		t.Error("no SetApprovalMode callback → no approval picker")
	}
}

func TestApplyApprovalModeSwitchesLiveSessionOnly(t *testing.T) {
	gate := core.NewPolicyGate(&core.PermissionPolicy{Mode: core.ApprovalYolo, ReadOnly: core.NewReadOnlySet("read"), EditTools: map[string]bool{"edit": true}}, nil)
	var rebuilt core.ApprovalMode
	i := newApprovalTestInteractive(gate, &fakeSettingsStore{}, &rebuilt)

	i.applyApprovalModeSetting("plan")

	if gate.Mode() != core.ApprovalPlan {
		t.Errorf("gate mode = %s, want plan (enforcement swapped live)", gate.Mode())
	}
	if rebuilt != core.ApprovalPlan {
		t.Errorf("registry not rebuilt for new mode (got %s)", rebuilt)
	}
	// And the status-bar label tracks it.
	if i.approvalModeLabel() != "plan" {
		t.Errorf("status label = %q, want plan", i.approvalModeLabel())
	}
	// Session-only: the picker must not write the persistent default —
	// that comes only from explicit config / --approval. (The store
	// has no approval field precisely because nothing should call it.)
}

func TestBuildPermissionsViewShowsModeRulesAndGrants(t *testing.T) {
	pol := &core.PermissionPolicy{
		Mode: core.ApprovalAsk,
		Rules: []core.PermissionRule{
			{Tool: "bash", Decision: core.RuleAllow, Source: "user"},
			{Tool: "web_fetch_raw", Args: regexp.MustCompile("169"), Decision: core.RuleDeny, Reason: "metadata", Source: "extension web"},
		},
		ReadOnly: core.NewReadOnlySet("read"), EditTools: map[string]bool{},
	}
	gate := core.NewPolicyGate(pol, confirmStub{ConfirmDecision: core.ConfirmDecision{Allow: true, RememberTool: true}})
	// Build a session grant.
	gate.Check("edit", nil, "")

	i := &Interactive{turns: newTurnEngine(), cfg: InteractiveConfig{ConfirmGate: gate, Theme: tui.Dark}}
	info, grants := i.buildPermissionsView()
	text := strings.Join(info, "\n")

	for _, want := range []string{"approval mode", "ask", "[user]", "bash", "allow", "[extension web]", "web_fetch_raw", "deny", "metadata", "this session"} {
		if !strings.Contains(text, want) {
			t.Errorf("permissions view missing %q\n---\n%s", want, text)
		}
	}
	// The session grant for "edit" surfaces as a revocable list entry.
	if len(grants) != 1 || grants[0].tool != "edit" {
		t.Errorf("grants = %+v, want one entry for edit", grants)
	}
}

func TestBuildPermissionsViewNoGate(t *testing.T) {
	i := &Interactive{turns: newTurnEngine(), cfg: InteractiveConfig{Theme: tui.Dark}}
	info, _ := i.buildPermissionsView()
	text := strings.Join(info, "\n")
	if !strings.Contains(text, "yolo") {
		t.Errorf("no-gate view should mention yolo, got: %s", text)
	}
}

// confirmStub is a Confirmer returning a fixed decision.
type confirmStub struct{ core.ConfirmDecision }

func (c confirmStub) Confirm(string, string) core.ConfirmDecision { return c.ConfirmDecision }

func TestApplyApprovalModeRejectsBadValue(t *testing.T) {
	gate := core.NewPolicyGate(&core.PermissionPolicy{Mode: core.ApprovalYolo, ReadOnly: core.NewReadOnlySet(), EditTools: map[string]bool{}}, nil)
	var rebuilt core.ApprovalMode
	i := newApprovalTestInteractive(gate, &fakeSettingsStore{}, &rebuilt)

	i.applyApprovalModeSetting("bogus")
	if gate.Mode() != core.ApprovalYolo || rebuilt != "" {
		t.Error("an invalid mode must not switch anything")
	}
}
