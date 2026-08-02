package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

const seats = 3

// autoPanelConfig builds a config whose draw is BOUNDED by an explicit
// provider list. Bounding it is what makes these tests hermetic: the
// credential check reads the real environment and the real auth store, so an
// unbounded draw would pick up whatever the machine running the test happens
// to be logged into.
func autoPanelConfig(t *testing.T, providers ...string) config.Config {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	// Every env var the bounded providers below could resolve from, cleared
	// first so a test says exactly which providers exist.
	for _, ev := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "DEEPSEEK_API_KEY",
		"KIMI_API_KEY", "MOONSHOT_API_KEY", "COPILOT_GITHUB_TOKEN",
		"GITHUB_COPILOT_TOKEN",
	} {
		t.Setenv(ev, "")
	}
	var uc config.Config
	uc.Raati.AutoPanel = true
	uc.Raati.AutoPanelProviders = providers
	return uc
}

func login(t *testing.T, envVar string) {
	t.Helper()
	t.Setenv(envVar, "test-key-not-a-real-credential")
}

func seatedProviders(panel []raati.Binding) []string {
	out := make([]string, 0, len(panel))
	for _, b := range panel {
		out = append(out, b.Provider)
	}
	return out
}

func whyNot(cands []AutoPanelCandidate, provider string) string {
	for _, c := range cands {
		if c.Provider == provider {
			return c.Why
		}
	}
	return "(not considered)"
}

// The flag IS the feature. Without it nothing resolves, nothing is offered,
// and level 2 stays exactly as unavailable as it was — even on a machine
// logged into every provider terva knows.
func TestAutoPanelIsOffUntilAskedFor(t *testing.T) {
	uc := autoPanelConfig(t, "anthropic", "google", "deepseek")
	login(t, "ANTHROPIC_API_KEY")
	login(t, "GEMINI_API_KEY")
	login(t, "DEEPSEEK_API_KEY")

	uc.Raati.AutoPanel = false
	panel, cands := AutoRaatiPanel(uc, seats)
	if panel != nil || cands != nil {
		t.Fatalf("auto panel resolved with the flag off: %v", panel)
	}
	// And the choke point every convening surface reads agrees.
	if got := RaatiLevel2Bindings(uc); len(got) != 0 {
		t.Fatalf("RaatiLevel2Bindings seated %v with the flag off", got)
	}
	if lvl := tools.HighestRaatiLevel("anthropic", nil, RaatiLevel2Bindings(uc), seats); lvl == 2 {
		t.Error("level 2 became available with the flag off")
	}

	uc.Raati.AutoPanel = true
	if panel, _ := AutoRaatiPanel(uc, seats); len(panel) != seats {
		t.Fatalf("flag on seated %d of %d", len(panel), seats)
	}
}

func TestAutoPanelSeatsOneProviderPerLineage(t *testing.T) {
	uc := autoPanelConfig(t, "anthropic", "google", "deepseek")
	login(t, "ANTHROPIC_API_KEY")
	login(t, "GEMINI_API_KEY")
	login(t, "DEEPSEEK_API_KEY")

	panel, cands := AutoRaatiPanel(uc, seats)
	if len(panel) != seats {
		t.Fatalf("seated %v, want %d seats (%+v)", seatedProviders(panel), seats, cands)
	}
	lineages := map[string]bool{}
	for _, b := range panel {
		if b.Model == "" {
			t.Errorf("seat %s has no model", b.Provider)
		}
		l := tools.ModelLineage(b.Model)
		if lineages[l] {
			t.Errorf("two seats share the %q lineage: %v", l, panel)
		}
		lineages[l] = true
	}
}

// The failure this whole check exists for: Anthropic and GitHub Copilot are
// two logins, two bills, and one set of weights. A panel of three Claudes is
// rigor level 0 with level 2's label on it, and a gate would be trusted on it.
func TestAutoPanelRefusesToSeatTheSameWeightsTwice(t *testing.T) {
	uc := autoPanelConfig(t, "anthropic", "github-copilot", "google")
	login(t, "ANTHROPIC_API_KEY")
	login(t, "COPILOT_GITHUB_TOKEN")
	login(t, "GEMINI_API_KEY")

	panel, cands := AutoRaatiPanel(uc, seats)
	if panel != nil {
		t.Fatalf("seated %v — two of those are Claude", panel)
	}
	why := whyNot(cands, "github-copilot")
	if !strings.Contains(why, "anthropic") || !strings.Contains(why, "claude") {
		t.Errorf("copilot was passed over for the wrong reason: %q", why)
	}
}

// Under-filling refuses. A two-seat panel is not a weaker three-seat panel,
// and padding the third seat from a provider already present rebuilds exactly
// the correlation the level rules out.
func TestAutoPanelRefusesToUnderfill(t *testing.T) {
	uc := autoPanelConfig(t, "anthropic", "google", "deepseek")
	login(t, "ANTHROPIC_API_KEY")
	login(t, "GEMINI_API_KEY")
	// deepseek deliberately not logged in.

	panel, cands := AutoRaatiPanel(uc, seats)
	if panel != nil {
		t.Fatalf("seated %v from two providers", panel)
	}
	if why := whyNot(cands, "deepseek"); !strings.Contains(why, "logged in") {
		t.Errorf("deepseek skipped for the wrong reason: %q", why)
	}
	// And the report says so rather than showing an empty pane.
	rep := AutoPanelReport(uc, seats)
	if !strings.Contains(rep, "cannot fill") || !strings.Contains(rep, "deepseek") {
		t.Errorf("report does not explain the refusal:\n%s", rep)
	}
}

// A provider with no strong rung and no override cannot be seated: there is
// nothing to seat. The message points at the config key that fixes it.
func TestAutoPanelSkipsAProviderWithNoStrongTier(t *testing.T) {
	uc := autoPanelConfig(t, "openrouter", "anthropic", "google")
	t.Setenv("OPENROUTER_API_KEY", "test-key-not-a-real-credential")
	login(t, "ANTHROPIC_API_KEY")
	login(t, "GEMINI_API_KEY")

	_, cands := AutoRaatiPanel(uc, seats)
	if why := whyNot(cands, "openrouter"); !strings.Contains(why, "swarm_tiers") {
		t.Errorf("openrouter skipped without naming the fix: %q", why)
	}

	// …and a swarm_tiers override is exactly that fix.
	uc.SwarmTiers = map[string]config.TierConfig{
		"openrouter": {Strong: config.TierRung{Model: "moonshotai/kimi-k2.6"}},
	}
	panel, cands := AutoRaatiPanel(uc, seats)
	if len(panel) != seats {
		t.Fatalf("override did not seat openrouter: %v (%+v)", panel, cands)
	}
	if panel[0].Provider != "openrouter" || panel[0].Model != "moonshotai/kimi-k2.6" {
		t.Errorf("first seat = %+v, want the override", panel[0])
	}
}

// An operator who wrote the seats down means it. A derivation must never
// overrule the thing it exists to substitute for.
func TestExplicitLevel2WinsOverTheAutoPanel(t *testing.T) {
	uc := autoPanelConfig(t, "anthropic", "google", "deepseek")
	login(t, "ANTHROPIC_API_KEY")
	login(t, "GEMINI_API_KEY")
	login(t, "DEEPSEEK_API_KEY")
	uc.Raati.Level2 = []config.RaatiBindingConfig{
		{Provider: "ollama", Model: "qwen3:32b"},
		{Provider: "ollama", Model: "qwen3:14b"},
		{Provider: "ollama", Model: "qwen3:4b"},
	}

	got := RaatiLevel2Bindings(uc)
	if len(got) != 3 || got[0].Provider != "ollama" {
		t.Fatalf("auto panel overruled an explicit raati.level2: %v", got)
	}
}

// The symptom that started this: the shipped code-review profile is a gate,
// a gate needs a decorrelated panel, and on a provider without a full tier
// ladder the only route to one was a hand-written raati.level2.
func TestAutoPanelLetsAGateConveneWithoutHandWrittenSeats(t *testing.T) {
	uc := autoPanelConfig(t, "anthropic", "google", "deepseek")
	login(t, "ANTHROPIC_API_KEY")
	login(t, "GEMINI_API_KEY")
	login(t, "DEEPSEEK_API_KEY")

	level2 := RaatiLevel2Bindings(uc)
	// deepseek's own ladder is two rungs, so without the auto panel this host
	// tops out at rigor 0 and the gate refuses itself.
	lvl := tools.HighestRaatiLevel("deepseek", nil, level2, seats)
	if lvl != 2 {
		t.Fatalf("highest level = %d, want 2 from the auto panel", lvl)
	}
	if err := tools.RefuseCorrelatedGate("code-review", raati.ClassGate, level2, true); err != nil {
		t.Fatalf("gate still refuses: %v", err)
	}
	if _, err := tools.ResolveRaatiBindings(lvl, "deepseek", "deepseek-v4-pro", "deepseek", nil, level2, seats); err != nil {
		t.Fatalf("level 2 does not seat: %v", err)
	}
}

func TestAutoPanelOrderPrefersTheOperatorsList(t *testing.T) {
	var uc config.Config
	uc.Raati.AutoPanelProviders = []string{"google", "  ", "anthropic"}
	if got := autoPanelOrder(uc); len(got) != 2 || got[0] != "google" || got[1] != "anthropic" {
		t.Errorf("order = %v, want the operator's list with blanks dropped", got)
	}

	uc.Raati.AutoPanelProviders = nil
	uc.SwarmTiers = map[string]config.TierConfig{"my-local-box": {Strong: config.TierRung{Model: "x"}}}
	got := autoPanelOrder(uc)
	if len(got) < len(ProviderIDs()) {
		t.Fatalf("registry order lost providers: %v", got)
	}
	if got[len(got)-1] != "my-local-box" {
		t.Errorf("a tier-configured non-registry provider was dropped: %v", got[len(got)-1])
	}
}

func TestModelLineage(t *testing.T) {
	same := [][2]string{
		{"claude-opus-4-1", "claude-opus-4.5"},               // anthropic vs copilot
		{"claude-sonnet-4-5", "anthropic.claude-sonnet-4-6"}, // vs bedrock
		{"k3", "kimi-k2.6"},                                  // kimi vs a gateway's moonshot
		{"gpt-5", "gpt-5.6-sol"},                             // openai vs codex
		{"gemini-2.5-pro", "gemini-3-flash-preview"},
	}
	for _, p := range same {
		if a, b := tools.ModelLineage(p[0]), tools.ModelLineage(p[1]); a != b {
			t.Errorf("%q(%s) and %q(%s) should share a lineage", p[0], a, p[1], b)
		}
	}
	differ := [][2]string{
		{"claude-opus-4-1", "gemini-2.5-pro"},
		{"gpt-5", "deepseek-v4-pro"},
		{"k3", "glm-5.2"},
		{"minimax-m3", "qwen3.6-plus"},
	}
	for _, p := range differ {
		if a, b := tools.ModelLineage(p[0]), tools.ModelLineage(p[1]); a == b {
			t.Errorf("%q and %q both resolved to lineage %q", p[0], p[1], a)
		}
	}
	// An id terva knows nothing about is its own lineage — permissive, and
	// the only honest answer available.
	if got := tools.ModelLineage("some-local-finetune-v3"); got != "some-local-finetune-v3" {
		t.Errorf("unknown model lineage = %q, want the id itself", got)
	}
	if got := tools.ModelLineage(""); got != "" {
		t.Errorf("empty model lineage = %q", got)
	}
}
