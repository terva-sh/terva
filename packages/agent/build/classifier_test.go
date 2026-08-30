package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// isolateHome points TERVA_HOME at an empty directory so tier resolution sees
// no user overrides and the built-in family tables alone decide. Without it the
// result would depend on whoever runs the tests.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
}

// seedUserConfig points TERVA_HOME at a fresh directory holding exactly this
// config.json, so InstallClassifier's own LoadConfig reads it.
func seedUserConfig(t *testing.T, body string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
}

// collectNotes installs a classifier and returns everything it said.
//
// The notes ARE the observable: whether screening switched on is otherwise
// invisible in a home with no credential, because a classifier that cannot
// reach a model installs nothing either way. An off mode returns before it
// warns about anything; any enabled mode runs on to model and credential
// resolution and says why it gave up. So a note means "it was switched on",
// and silence means "it was not" — testable without a credential.
func collectNotes(t *testing.T, host Resolved, override string) (*core.ConfirmGate, []string) {
	t.Helper()
	var notes []string
	g := core.NewConfirmGate(nil)
	InstallClassifier(g, host, override, func(f string, a ...any) {
		notes = append(notes, fmt.Sprintf(f, a...))
	})
	return g, notes
}

// --classifier must beat the config key in BOTH directions. Turning screening
// off for one run matters as much as turning it on: it is the cheapest way out
// when a screener starts refusing something it should not, and a user who has
// to edit config to get their work done will edit it once and never put it
// back.
func TestClassifierFlagOverridesConfigMode(t *testing.T) {
	host := Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}

	cases := []struct {
		name, cfg, override string
		wantEnabled         bool
	}{
		{"no config, no flag", `{}`, "", false},
		{"flag enables over a config that never mentions it", `{}`, "screen", true},
		{"flag enables over an explicit off", `{"classifier":{"mode":"off"}}`, "screen", true},
		{"flag disables a config that enabled it", `{"classifier":{"mode":"approve"}}`, "off", false},
		{"no flag leaves an enabled config alone", `{"classifier":{"mode":"screen"}}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedUserConfig(t, tc.cfg)
			_, notes := collectNotes(t, host, tc.override)
			if gotEnabled := len(notes) > 0; gotEnabled != tc.wantEnabled {
				t.Fatalf("screening enabled = %v, want %v\nnotes: %v", gotEnabled, tc.wantEnabled, notes)
			}
		})
	}
}

// The override carries a MODE and nothing else, and that is the trust
// boundary. argv may choose among the postures config already permits; it may
// NOT choose which model does the screening, or where it is reached. If a flag
// could redirect the provider, `--classifier` would become a way to point
// screening at an endpoint of someone else's choosing.
//
// Proven in one shot: the config names a provider that cannot resolve, so if
// the flag enabled screening (it must) the failure has to name THAT provider
// (proving the flag did not supply its own).
func TestClassifierFlagOverridesOnlyTheMode(t *testing.T) {
	seedUserConfig(t, `{"classifier":{"mode":"off","provider":"nonexistent-provider"}}`)

	g, notes := collectNotes(t, Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}, "screen")

	joined := strings.Join(notes, "\n")
	if len(notes) == 0 {
		t.Fatal("the flag did not switch screening on; the rest of this test proves nothing")
	}
	if !strings.Contains(joined, "nonexistent-provider") {
		t.Fatalf("screening did not use the CONFIG's provider; argv must not redirect it.\nnotes: %s", joined)
	}
	if got := g.ClassifierMode(); got != core.ClassifierOff {
		t.Fatalf("gate reports %q after an unresolvable provider, want off", got)
	}
}

// A mistyped mode is refused at PARSE time rather than warned about later.
//
// Every other classifier failure abstains into "screening stays off", which is
// right for a provider blip and wrong for a typo: `--classifier=sceen` is an
// unambiguous request for screening, and answering it with a startup line that
// scrolls past would leave the user believing they had it.
func TestParseArgsClassifier(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"screen", "screen", "screen", false},
		{"approve", "approve", "approve", false},
		{"off is a choice, not an absence", "off", "off", false},
		{"case folds", "SCREEN", "screen", false},
		{"a typo is refused", "sceen", "", true},
		{"a neighbouring word is refused", "approved", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := ParseArgs([]string{"--classifier", tc.in})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("--classifier %q was accepted; a typo must not read as off", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("--classifier %q: %v", tc.in, err)
			}
			if a.Classifier != tc.want {
				t.Fatalf("Classifier = %q, want %q", a.Classifier, tc.want)
			}
		})
	}

	// Absent means absent: the config decides, and nothing is overridden.
	a, err := ParseArgs(nil)
	if err != nil {
		t.Fatalf("ParseArgs(nil): %v", err)
	}
	if a.Classifier != "" {
		t.Fatalf("Classifier = %q with no flag, want empty", a.Classifier)
	}
}

// THE cost property. Screening runs on gated calls for a whole session, so
// defaulting to the host model would be a silent, permanent bill — and it is
// the shape you get by turning the feature on the obvious way, with no model
// configured at all.
func TestClassifierDefaultsToTheCheapRung(t *testing.T) {
	isolateHome(t)
	host := Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}

	_, model, _, err := classifierTarget(host, config.Config{})
	if err != nil {
		t.Fatalf("classifierTarget: %v", err)
	}
	if model == host.Model {
		t.Fatalf("screening resolved to the host model %q; the weak rung should have won", model)
	}
	if !strings.Contains(strings.ToLower(model), "haiku") {
		t.Fatalf("model = %q, want anthropic's weak (haiku) rung", model)
	}
}

// A provider with neither a built-in table nor an override resolves nothing.
// Quietly using the host model there would rebuild the invisible per-call bill
// the ordering above exists to prevent, so it refuses and says how to fix it.
func TestClassifierRefusesRatherThanBillingTheHost(t *testing.T) {
	isolateHome(t)
	gateway := Resolved{Provider: "openrouter", Model: "some/expensive-model"}

	_, _, _, err := classifierTarget(gateway, config.Config{})
	if err == nil {
		t.Fatal("an unresolvable tier was accepted; it must refuse rather than bill the host model")
	}
	for _, want := range []string{"swarm_tiers", "host_model"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not tell the operator about %q: %v", want, err)
		}
	}
}

// The escape hatch exists but is explicit, matching the convention that
// anything costing the user something stays off until they say so.
func TestClassifierHostModelIsOptIn(t *testing.T) {
	isolateHome(t)
	gateway := Resolved{Provider: "openrouter", Model: "some/expensive-model"}

	_, model, _, err := classifierTarget(gateway, config.Config{
		Classifier: config.ClassifierConfig{HostModel: true},
	})
	if err != nil {
		t.Fatalf("classifierTarget with host_model: %v", err)
	}
	if model != gateway.Model {
		t.Fatalf("model = %q, want the host model %q", model, gateway.Model)
	}
}

// A user's swarm_tiers override wins over the built-in guess, which is what
// makes the gateways configurable at all.
func TestClassifierHonoursASwarmTiersOverride(t *testing.T) {
	isolateHome(t)
	gateway := Resolved{Provider: "openrouter", Model: "some/expensive-model"}

	_, model, _, err := classifierTarget(gateway, config.Config{
		SwarmTiers: map[string]config.TierConfig{
			"openrouter": {Weak: config.TierRung{Model: "cheap/tiny"}},
		},
	})
	if err != nil {
		t.Fatalf("classifierTarget: %v", err)
	}
	if model != "cheap/tiny" {
		t.Fatalf("model = %q, want the configured weak rung", model)
	}
}

// A rung may name only an EFFORT, meaning "the built-in model for this rung,
// but think this hard". The resolver fills the model in from the built-in
// family table, so the effort must survive alongside it rather than being
// dropped on the way to the request.
func TestClassifierAcceptsAnEffortOnlyRung(t *testing.T) {
	isolateHome(t)
	host := Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}

	_, model, reasoning, err := classifierTarget(host, config.Config{
		SwarmTiers: map[string]config.TierConfig{
			"anthropic": {Weak: config.TierRung{Reasoning: "off"}},
		},
	})
	if err != nil {
		t.Fatalf("classifierTarget: %v", err)
	}
	if !strings.Contains(strings.ToLower(model), "haiku") {
		t.Fatalf("model = %q, want the built-in weak rung filled in behind the effort", model)
	}
	if reasoning != "off" {
		t.Fatalf("reasoning = %q, want the rung's own effort", reasoning)
	}
}

// An effort-only rung on a provider with NO built-in family table cannot
// resolve: there is no model to attach the effort to. It must refuse rather
// than silently fall back to the host model.
//
// This is the case that proved classifierTarget's old "effort means the host
// model" branch was dead code — overridePick fills the model in or returns
// nothing, so a pick with an effort and no model never reaches a caller.
func TestClassifierEffortOnlyRungNeedsABuiltinModel(t *testing.T) {
	isolateHome(t)
	host := Resolved{Provider: "openrouter", Model: "only/model"}

	if _, _, _, err := classifierTarget(host, config.Config{
		SwarmTiers: map[string]config.TierConfig{
			"openrouter": {Weak: config.TierRung{Reasoning: "off"}},
		},
	}); err == nil {
		t.Fatal("an effort-only rung with no built-in model resolved; it must refuse rather than bill the host")
	}
}

// Off is the default, and an unparseable mode must fail to OFF rather than
// guessing — a half-understood permission setting is worse than none.
func TestClassifierForStaysOffUnlessAsked(t *testing.T) {
	isolateHome(t)
	r := Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}

	for _, tc := range []struct{ name, mode string }{
		{"absent", ""},
		{"explicitly off", "off"},
		{"garbage", "sure-why-not"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cls, mode := ClassifierFor(r, config.Config{
				Classifier: config.ClassifierConfig{Mode: tc.mode},
			}, nil)
			if cls != nil || mode != core.ClassifierOff {
				t.Fatalf("mode %q produced (%v, %q), want (nil, off)", tc.mode, cls != nil, mode)
			}
		})
	}
}

// A mode that parses but has no credential behind it must also fail to OFF,
// and say so — silently doing nothing is how a safety net is believed in but
// absent.
func TestClassifierForRefusesWithoutACredential(t *testing.T) {
	isolateHome(t)
	var notes []string
	cls, mode := ClassifierFor(
		Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}, // no Credential
		config.Config{Classifier: config.ClassifierConfig{Mode: "screen"}},
		func(f string, a ...any) { notes = append(notes, f) },
	)
	if cls != nil || mode != core.ClassifierOff {
		t.Fatalf("got (%v, %q), want (nil, off) with no credential", cls != nil, mode)
	}
	if len(notes) == 0 {
		t.Fatal("refusing to screen produced no note; the operator would believe it was on")
	}
}

// InstallClassifier must leave a gate untouched when nothing built, so every
// failure path above is indistinguishable from the feature not existing.
func TestInstallClassifierLeavesTheGateAloneOnFailure(t *testing.T) {
	isolateHome(t)
	g := core.NewConfirmGate(nil)
	InstallClassifier(g, Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}, "", func(string, ...any) {})
	if got := g.ClassifierMode(); got != core.ClassifierOff {
		t.Fatalf("gate reports %q after a failed install, want off", got)
	}
	// And a nil gate must not panic: headless yolo builds none at all.
	InstallClassifier(nil, Resolved{}, "", nil)
}
