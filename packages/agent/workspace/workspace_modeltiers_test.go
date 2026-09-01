package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// The view has to answer for a rung nobody pinned. An empty swarm_tiers is the
// normal case and says nothing about whether the ladder is right — google's
// medium and strong rungs resolved to image-generation models with config
// completely empty — so a view that returned only overrides would have shown
// three blank rungs on the day that was live.
func TestModelTiersShowsTheResolvedLadderWithNoOverrides(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}

	got, err := w.ModelTiers(context.Background(), ctrlproto.ModelTiersParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("ModelTiers: %v", err)
	}
	if got.HasOverride {
		t.Error("HasOverride with nothing in config")
	}
	if len(got.Rungs) != 4 {
		t.Fatalf("rungs = %d, want 4 — the three capability rungs and the cost tier", len(got.Rungs))
	}
	// Capability ladder first, cost tier last, so the three still read as a
	// ladder on every surface that renders them in order.
	for i, want := range []string{"weak", "medium", "strong", "cheap"} {
		if got.Rungs[i].Rung != want {
			t.Errorf("rung %d = %q, want %q", i, got.Rungs[i].Rung, want)
		}
		if got.Rungs[i].Model == "" || got.Rungs[i].Source != "built-in" {
			t.Errorf("%s = %q from %q, want a built-in pick", want, got.Rungs[i].Model, got.Rungs[i].Source)
		}
		if got.Rungs[i].Label == "" {
			t.Errorf("%s has no label; a client should not have to re-read the catalog", want)
		}
	}
}

// Pinning a rung, and taking the pin back off again.
func TestModelTiersSetAndReset(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}
	ctx := context.Background()

	before, err := w.ModelTiers(ctx, ctrlproto.ModelTiersParams{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	builtinWeak, builtinWeakEffort := before.Rungs[0].Model, before.Rungs[0].Reasoning

	// A rung naming ONLY a level: keep the built-in model, think differently.
	// This is the cheap way to build a ladder on a provider terva already knows,
	// and it must not force the id to be repeated (where it would then drift).
	if err := w.ModelTiersSet(ctx, ctrlproto.ModelTiersSetParams{
		Provider: "anthropic", Rung: "weak", Reasoning: "off",
	}); err != nil {
		t.Fatalf("ModelTiersSet(reasoning only): %v", err)
	}
	got, err := w.ModelTiers(ctx, ctrlproto.ModelTiersParams{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasOverride {
		t.Error("HasOverride is false after pinning a rung")
	}
	if got.Rungs[0].Model != builtinWeak {
		t.Errorf("weak model = %q, want the built-in %q kept", got.Rungs[0].Model, builtinWeak)
	}
	if got.Rungs[0].Reasoning != "off" || got.Rungs[0].Source != "override" {
		t.Errorf("weak = (%q, %q), want (off, override)", got.Rungs[0].Reasoning, got.Rungs[0].Source)
	}

	// Reset that rung: the entry empties out, so HasOverride must go back to
	// false rather than reporting an empty husk forever.
	if err := w.ModelTiersReset(ctx, ctrlproto.ModelTiersResetParams{Provider: "anthropic", Rung: "weak"}); err != nil {
		t.Fatalf("ModelTiersReset: %v", err)
	}
	got, err = w.ModelTiers(ctx, ctrlproto.ModelTiersParams{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	if got.HasOverride {
		t.Error("HasOverride still true after the last rung was reset")
	}
	// Back to the BUILT-IN pick, effort included. Not back to "no effort": a
	// built-in rung may name one now (anthropic's weak rung is a haiku that
	// thinks hard), so reset means "whatever the table says", not "blank".
	if got.Rungs[0].Source != "built-in" || got.Rungs[0].Reasoning != builtinWeakEffort {
		t.Errorf("weak = (%q, %q) after reset, want the built-in pick back (%q)",
			got.Rungs[0].Source, got.Rungs[0].Reasoning, builtinWeakEffort)
	}
	cfg, _ := config.LoadConfig()
	if _, ok := cfg.SwarmTiers["anthropic"]; ok {
		t.Error("an entry with no rungs left should be dropped, not kept empty")
	}
}

// Setting nothing at all is a reset, not a rung pinned to blank — a blank rung
// in the file would read as a pin and resolve to nothing.
func TestModelTiersSetWithNothingClearsTheRung(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}
	ctx := context.Background()

	if err := w.ModelTiersSet(ctx, ctrlproto.ModelTiersSetParams{Provider: "anthropic", Rung: "strong", Reasoning: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := w.ModelTiersSet(ctx, ctrlproto.ModelTiersSetParams{Provider: "anthropic", Rung: "strong"}); err != nil {
		t.Fatalf("empty set: %v", err)
	}
	cfg, _ := config.LoadConfig()
	if _, ok := cfg.SwarmTiers["anthropic"]; ok {
		t.Error("an empty set should clear the rung, not write a blank pin")
	}
}

// A rung naming a model this provider does not have would resolve to nothing,
// and the sub-agent would quietly inherit the host model — the exact kind of
// silent wrong answer this surface exists to end. Refuse it at the door.
func TestModelTiersSetRefusesAnUnknownModel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}
	err := w.ModelTiersSet(context.Background(), ctrlproto.ModelTiersSetParams{
		Provider: "anthropic", Rung: "weak", Model: "no-such-model-anywhere",
	})
	if err == nil {
		t.Fatal("pinning a model the provider does not have was accepted")
	}
	cfg, _ := config.LoadConfig()
	if _, ok := cfg.SwarmTiers["anthropic"]; ok {
		t.Error("a refused set still wrote to config")
	}
}

// Bad rung names are refused rather than silently ignored, on both verbs.
func TestModelTiersRejectsUnknownRungs(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}
	ctx := context.Background()
	if err := w.ModelTiersSet(ctx, ctrlproto.ModelTiersSetParams{Provider: "anthropic", Rung: "gigantic", Reasoning: "high"}); err == nil {
		t.Error("set accepted an unknown rung")
	}
	if err := w.ModelTiersReset(ctx, ctrlproto.ModelTiersResetParams{Provider: "anthropic", Rung: "gigantic"}); err == nil {
		t.Error("reset accepted an unknown rung")
	}
	if _, err := w.ModelTiers(ctx, ctrlproto.ModelTiersParams{}); err == nil {
		t.Error("the view accepted an empty provider")
	}
}
