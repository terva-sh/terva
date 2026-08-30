package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

func findSetting(sv ctrlproto.SettingsView, key string) *ctrlproto.SettingItem {
	for i := range sv.Items {
		if sv.Items[i].Key == key {
			return &sv.Items[i]
		}
	}
	return nil
}

// The reasoning-summary control is an enum offering off plus the three detail
// levels, defaults to off, persists, and is reflected back by the view — the
// one surface both the TUI dialog and the web pane render from.
func TestSettingsReasoningSummary(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTestSession()
	s.ws = &Workspace{}

	it := findSetting(s.settingsView(), "reasoning_summary")
	if it == nil || it.Type != "enum" {
		t.Fatalf("no reasoning_summary enum row in the settings view: %+v", s.settingsView().Items)
	}
	// Recording used to be opt-in. It is now on by default: every thinking
	// display reads persisted text, so with recording off the TUI block, ctrl+r,
	// the web disclosure and the copy picker's think parts all have nothing to
	// show once the turn ends.
	if it.Value != config.DefaultReasoningSummary {
		t.Errorf("default = %q, want %q", it.Value, config.DefaultReasoningSummary)
	}
	want := map[string]bool{"": true, "auto": true, "concise": true, "detailed": true}
	if len(it.Options) != len(want) {
		t.Errorf("options = %+v, want exactly %d", it.Options, len(want))
	}
	for _, o := range it.Options {
		if !want[o.Value] {
			t.Errorf("unexpected option %q", o.Value)
		}
		if o.Label == "" {
			t.Errorf("option %q has no label; the TUI picker and web dropdown both render it", o.Value)
		}
	}

	if err := s.settingsAction("set", map[string]string{"key": "reasoning_summary", "value": "concise"}); err != nil {
		t.Fatalf("set reasoning_summary: %v", err)
	}
	if cfg, _ := config.LoadConfig(); cfg.ReasoningSummary == nil || *cfg.ReasoningSummary != "concise" {
		t.Errorf("config.ReasoningSummary = %v, want concise", cfg.ReasoningSummary)
	}
	if it := findSetting(s.settingsView(), "reasoning_summary"); it == nil || it.Value != "concise" {
		t.Errorf("the view should reflect concise, got %+v", it)
	}

	// Turning it back off must round-trip: the empty value is a real choice
	// here, not "unset", and an enum that cannot return to off would strand
	// reasoning on disk.
	//
	// This is what the pointer buys, and it matters more now that the default is
	// on. An absent key resolves to "auto", so a chosen off has to be written as
	// an explicit empty string; recorded as absence it would read straight back
	// as the default and silently keep writing reasoning to disk.
	if err := s.settingsAction("set", map[string]string{"key": "reasoning_summary", "value": ""}); err != nil {
		t.Fatalf("set reasoning_summary off: %v", err)
	}
	cfg, _ := config.LoadConfig()
	if cfg.ReasoningSummary == nil {
		t.Fatal("turning it off left the key absent, which now reads back as the default")
	}
	if *cfg.ReasoningSummary != "" {
		t.Errorf("config.ReasoningSummary = %q, want empty after turning off", *cfg.ReasoningSummary)
	}
	if got := cfg.ReasoningSummaryMode(); got != "" {
		t.Errorf("ReasoningSummaryMode() = %q after choosing off, want it honoured", got)
	}
	if it := findSetting(s.settingsView(), "reasoning_summary"); it == nil || it.Value != "" {
		t.Errorf("the view should reflect off, got %+v", it)
	}
}

// Persisting is not enough: the change must reach every live session's agent,
// or the toggle reads as applied while the running session keeps recording
// (or not recording) whatever it started with.
func TestSettingsReasoningSummaryAppliesToLiveAgents(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	s := &wsSession{id: "s1", ws: w, hub: newWSHub()}
	w.sessions[s.id] = s
	s.agent = core.NewAgent(&gatedTurnClient{}, "m", "sys", core.Registry{})

	if s.agent.ReasoningSummary != "" {
		t.Fatalf("a fresh agent should not record reasoning, got %q", s.agent.ReasoningSummary)
	}
	if err := s.settingsAction("set", map[string]string{"key": "reasoning_summary", "value": "detailed"}); err != nil {
		t.Fatalf("set reasoning_summary: %v", err)
	}
	if s.agent.ReasoningSummary != "detailed" {
		t.Errorf("live agent = %q, want detailed — the change must not wait for a rebuild", s.agent.ReasoningSummary)
	}

	if err := s.settingsAction("set", map[string]string{"key": "reasoning_summary", "value": ""}); err != nil {
		t.Fatalf("set reasoning_summary off: %v", err)
	}
	if s.agent.ReasoningSummary != "" {
		t.Errorf("live agent = %q, want off — turning it back off must apply live too", s.agent.ReasoningSummary)
	}
}

// A value outside the enum is refused rather than persisted: it would ride
// every request on the codex path and be rejected by the backend each turn.
func TestSettingsReasoningSummaryRejectsUnknown(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTestSession()
	s.ws = &Workspace{}

	if err := s.settingsAction("set", map[string]string{"key": "reasoning_summary", "value": "verbose"}); err == nil {
		t.Fatal("an unknown reasoning_summary value was accepted")
	}
	// Nil, not empty: a refused value writes nothing at all, so the key stays
	// absent. An explicit empty string would be a deliberate off, which is a
	// different thing and would survive as one.
	if cfg, _ := config.LoadConfig(); cfg.ReasoningSummary != nil {
		t.Errorf("a refused value was still persisted: %q", *cfg.ReasoningSummary)
	}
}
