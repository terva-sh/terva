package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Hiding a model is a real block, not a display filter — but the block has to
// land in exactly one place, and these tests pin which.
//
// The danger the scoping exists to avoid is bricking a launch. If hiding
// filtered the catalogue, or refused every path that names a hidden model, then
// tidying a picker could stop terva from starting and stop a conversation from
// reopening. So the rule is: the CONFIGURED DEFAULT errors (a contradiction the
// user created and only they can settle), and everything else still works.

func seedHidden(t *testing.T, cfg config.Config) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	provider.SetUserModels([]provider.Model{
		{Provider: "ollama", ID: "tidy-away", MaxOutput: 8192},
		{Provider: "ollama", ID: "kept", MaxOutput: 8192},
	})
	writeUserConfig(t, config.TervaHome(), cfg)
}

// The one case that errors, and it must say how to get out of it: an error that
// only reports the problem leaves the user with a terva that will not start and
// no idea which file to edit.
func TestHiddenConfiguredDefaultIsRefusedWithAnActionableError(t *testing.T) {
	seedHidden(t, config.Config{
		Provider:     "ollama",
		Model:        "tidy-away",
		HiddenModels: []string{"ollama/*"},
	})

	_, err := Resolve(Args{}, false)
	if err == nil {
		t.Fatal("a hidden model as the configured default must be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		"ollama/tidy-away", // which model
		"ollama/*",         // which rule hid it, so the cause is not a mystery
		"hidden_models",    // the setting to edit
		"config.json",      // the file it lives in
		"--model",          // the one-shot escape hatch
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error must mention %q so the user can act on it; got:\n%s", want, msg)
		}
	}
}

// Naming a model on the command line is a clearer statement of intent than a
// visibility rule written earlier, so it wins. Without this a hidden model
// would be unreachable from the CLI, which buys nothing.
func TestExplicitModelFlagBeatsAVisibilityRule(t *testing.T) {
	seedHidden(t, config.Config{
		Provider:     "ollama",
		Model:        "tidy-away",
		HiddenModels: []string{"ollama/*"},
	})

	r, err := Resolve(Args{Model: "tidy-away"}, false)
	if err != nil {
		t.Fatalf("--model on a hidden model must work: %v", err)
	}
	if r.Model != "tidy-away" {
		t.Errorf("Model = %q, want the model the flag named", r.Model)
	}
}

// The mechanical half of "a resume always works": a resume re-enters Resolve
// with the session's model in args.Model, which is the exempt path above.
// Refusing to reopen a conversation over a picker preference would be
// destructive out of all proportion to the setting.
func TestResumingOntoAHiddenModelStillResolves(t *testing.T) {
	seedHidden(t, config.Config{
		Provider:     "ollama",
		Model:        "kept",
		HiddenModels: []string{"ollama/tidy-away"},
	})

	// applyResumedModel builds exactly this: the session's stored pair, passed
	// explicitly rather than read from config.
	r, err := Resolve(Args{Provider: "ollama", Model: "tidy-away"}, false)
	if err != nil {
		t.Fatalf("a session stored on a hidden model must still resolve: %v", err)
	}
	if r.Model != "tidy-away" {
		t.Errorf("Model = %q, want the session's stored model", r.Model)
	}
}

// Nobody chose the built-in per-provider fallback, so it must not be able to
// fail a launch. A broad "hide everything" rule plus an empty config would
// otherwise leave terva unable to start at all.
//
// anthropic rather than ollama on purpose: this has to exercise the branch
// where DefaultModelForProvider supplies the model, and ollama has no built-in
// default (it fails earlier with "requires --model", which would pass this test
// for entirely the wrong reason).
func TestABroadRuleDoesNotBrickALaunchWithNoConfiguredModel(t *testing.T) {
	seedHidden(t, config.Config{
		Provider:     "anthropic",
		HiddenModels: []string{"*"}, // hide literally everything
	})

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("with no configured model the fallback must still launch, got: %v", err)
	}
	if r.Model == "" {
		t.Error("the fallback should have supplied a model id")
	}
}

// Hiding is about choosing, never about capability: the catalogue keeps the
// model, so context-window maths, cost accounting and the gauges all keep
// working for a session that is on one.
func TestHidingLeavesTheCatalogueIntact(t *testing.T) {
	seedHidden(t, config.Config{
		Provider:     "ollama",
		Model:        "kept",
		HiddenModels: []string{"ollama/tidy-away"},
	})

	if _, err := provider.FindModel("ollama", "tidy-away"); err != nil {
		t.Errorf("a hidden model must stay in the catalogue: %v", err)
	}
	r, err := Resolve(Args{Model: "tidy-away"}, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.MaxOutput != 8192 {
		t.Errorf("MaxOutput = %d, want the catalogue's 8192 — a hidden model\n"+
			"must keep the numbers every gauge and budget divides by", r.MaxOutput)
	}
}
