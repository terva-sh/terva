package raati

import (
	"strings"
	"testing"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func TestProfileFor(t *testing.T) {
	profs := map[string]Profile{
		"ethics":      {Description: "slow frontier panel"},
		"code-review": {Description: "gate-grade code panel"},
	}
	if p, err := ProfileFor(profs, "ethics"); err != nil || p.Description != "slow frontier panel" {
		t.Errorf("ProfileFor(ethics) = %+v, %v", p, err)
	}
	// Unknown names enumerate what IS configured, sorted, so the
	// caller can self-correct without reading the config.
	_, err := ProfileFor(profs, "nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "code-review, ethics") {
		t.Errorf("unknown-profile error = %v", err)
	}
	if _, err := ProfileFor(nil, "any"); err == nil || !strings.Contains(err.Error(), "no convening profiles configured") {
		t.Errorf("empty-set error = %v", err)
	}
}

func TestProfileEffectiveLevel(t *testing.T) {
	if lvl, ok := (Profile{}).EffectiveLevel(); ok || lvl != 0 {
		t.Errorf("empty profile level = %d, %v", lvl, ok)
	}
	// An explicit 0 is a deliberate cheap panel, not "unset".
	if lvl, ok := (Profile{Level: intPtr(0)}).EffectiveLevel(); !ok || lvl != 0 {
		t.Errorf("explicit level 0 = %d, %v", lvl, ok)
	}
	// Pinned seats imply the cross-provider level.
	if lvl, ok := (Profile{Seats: []Binding{{Provider: "p", Model: "m"}}}).EffectiveLevel(); !ok || lvl != 2 {
		t.Errorf("seats-implied level = %d, %v", lvl, ok)
	}
	// An explicit level wins over the seats implication.
	if lvl, ok := (Profile{Level: intPtr(1), Seats: []Binding{{}}}).EffectiveLevel(); !ok || lvl != 1 {
		t.Errorf("explicit level with seats = %d, %v", lvl, ok)
	}
}

func TestProfileValidSeats(t *testing.T) {
	full := Profile{Seats: []Binding{
		{Provider: "a", Model: "1"}, {Provider: "b", Model: "2"}, {Provider: "c", Model: "3"},
	}}
	if err := full.ValidSeats(3); err != nil {
		t.Errorf("full panel: %v", err)
	}
	if err := full.ValidSeats(4); err == nil || !strings.Contains(err.Error(), "pins 3 seat(s) but the panel has 4") {
		t.Errorf("count mismatch error = %v", err)
	}
	hollow := Profile{Seats: []Binding{{Provider: "a", Model: "1"}, {Provider: "b"}, {Provider: "c", Model: "3"}}}
	if err := hollow.ValidSeats(3); err == nil || !strings.Contains(err.Error(), "seat 2") {
		t.Errorf("hollow seat error = %v", err)
	}
}

func TestProfileNamesAndLine(t *testing.T) {
	profs := map[string]Profile{"b": {}, "a": {Description: "first"}}
	if got := strings.Join(ProfileNames(profs), ","); got != "a,b" {
		t.Errorf("ProfileNames = %q", got)
	}
	if got := ProfileLine("a", profs["a"]); got != "a — first" {
		t.Errorf("ProfileLine described = %q", got)
	}
	if got := ProfileLine("b", profs["b"]); got != "b" {
		t.Errorf("ProfileLine bare = %q", got)
	}
}

func TestProfilePickLevel(t *testing.T) {
	// Explicit level wins regardless of what the config supports.
	if lvl, ok, auto := (Profile{Level: intPtr(1), AutoLevel: true, AutoCeiling: 2}).PickLevel(2); lvl != 1 || !ok || auto {
		t.Errorf("explicit = %d,%v,%v", lvl, ok, auto)
	}
	// Pinned seats say level 2, not auto.
	if lvl, ok, auto := (Profile{Seats: []Binding{{Provider: "p", Model: "m"}}}).PickLevel(0); lvl != 2 || !ok || auto {
		t.Errorf("seats = %d,%v,%v", lvl, ok, auto)
	}
	// Auto rides the config's highest, capped by the ceiling.
	if lvl, ok, auto := (Profile{AutoLevel: true, AutoCeiling: 2}).PickLevel(2); lvl != 2 || !ok || !auto {
		t.Errorf("auto full = %d,%v,%v", lvl, ok, auto)
	}
	if lvl, _, _ := (Profile{AutoLevel: true, AutoCeiling: 1}).PickLevel(2); lvl != 1 {
		t.Errorf("auto ceiling = %d, want 1", lvl)
	}
	if lvl, _, auto := (Profile{AutoLevel: true, AutoCeiling: 2}).PickLevel(0); lvl != 0 || !auto {
		t.Errorf("auto floor = %d,%v", lvl, auto)
	}
	// No level story at all: the call decides.
	if _, ok, _ := (Profile{}).PickLevel(2); ok {
		t.Error("empty profile picked a level")
	}
}

func TestBuiltinProfiles(t *testing.T) {
	profs := BuiltinProfiles()
	if got := strings.Join(ProfileNames(profs), ","); got != "code-review,counsel,ethics,triage" {
		t.Fatalf("builtin set = %q", got)
	}
	for name, p := range profs {
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("%s has no description — it IS the selection signal", name)
		}
		if len(p.Seats) != 0 {
			t.Errorf("%s pins seats — built-ins can't know the user's providers", name)
		}
	}
	// triage is the deliberate correlated eight-ball; the serious three
	// ride auto so rigor climbs with the config.
	if tr := profs["triage"]; tr.Level == nil || *tr.Level != 0 || tr.SingleRound == nil || !*tr.SingleRound {
		t.Errorf("triage = %+v", tr)
	}
	for _, name := range []string{"counsel", "code-review", "ethics"} {
		if p := profs[name]; !p.AutoLevel || p.AutoCeiling != 2 {
			t.Errorf("%s not auto:2 = %+v", name, p)
		}
	}
	if profs["code-review"].Class != "gate" || profs["ethics"].Class != "veto" {
		t.Errorf("classes: code-review=%q ethics=%q", profs["code-review"].Class, profs["ethics"].Class)
	}
	if profs["ethics"].Inquire != "convener" || profs["counsel"].Inquire != "record" {
		t.Errorf("inquire: ethics=%q counsel=%q", profs["ethics"].Inquire, profs["counsel"].Inquire)
	}
}
