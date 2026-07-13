package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

func settingsItem(v ctrlproto.SettingsView, key string) (ctrlproto.SettingItem, bool) {
	for _, it := range v.Items {
		if it.Key == key {
			return it, true
		}
	}
	return ctrlproto.SettingItem{}, false
}

// An engine feature (build.EngineFeatures) projects into the settings pane:
// nested under lazy_tools, current value from the config override over the
// default, and a set both persists the override and flips every live
// session's agent immediately.
func TestSettingsEngineFeatureTogglesLiveAgents(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	s := &wsSession{id: "s1", ws: w, hub: newWSHub()}
	w.sessions[s.id] = s
	s.agent = core.NewAgent(&gatedTurnClient{}, "m", "sys", core.Registry{})
	s.agent.EnableLazyTools()
	if !s.agent.ActivationContinuationEnabled() {
		t.Fatal("activation continuation should default on under lazy tools")
	}

	// Hidden while lazy_tools is off; listed (default true) once it is on.
	if _, ok := settingsItem(s.settingsView(), "activation_continuation"); ok {
		t.Error("activation_continuation should be hidden while lazy_tools is off")
	}
	if err := config.MutateConfig(func(c *config.Config) { c.LazyTools = true }); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	item, ok := settingsItem(s.settingsView(), "activation_continuation")
	if !ok {
		t.Fatal("activation_continuation should be listed once lazy_tools is on")
	}
	if item.Type != "bool" || item.Value != "true" {
		t.Errorf("item = %+v; want a bool defaulting true", item)
	}

	// Setting it off persists the override and flips the live agent.
	if err := s.settingsAction("set", map[string]string{"key": "activation_continuation", "value": "false"}); err != nil {
		t.Fatalf("set activation_continuation false: %v", err)
	}
	if s.agent.ActivationContinuationEnabled() {
		t.Error("the live agent must flip immediately")
	}
	cfg, _ := config.LoadConfig()
	if on, ok := cfg.EngineFeatures["activation_continuation"]; !ok || on {
		t.Errorf("the override must persist, got %+v", cfg.EngineFeatures)
	}
	if item, _ = settingsItem(s.settingsView(), "activation_continuation"); item.Value != "false" {
		t.Errorf("the view must reflect the override, got %+v", item)
	}

	// Flipping back on applies live too.
	if err := s.settingsAction("set", map[string]string{"key": "activation_continuation", "value": "true"}); err != nil {
		t.Fatalf("set activation_continuation true: %v", err)
	}
	if !s.agent.ActivationContinuationEnabled() {
		t.Error("re-enabling must flip the live agent back on")
	}

	// A key that is neither a known setting nor an engine feature still errors.
	if err := s.settingsAction("set", map[string]string{"key": "nope", "value": "true"}); err == nil {
		t.Error("an unknown setting key must error")
	}
}
