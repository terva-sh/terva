package build

import (
	"io"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
)

// resolveCapturingStderr runs Resolve with stderr redirected, returning what it
// printed. Reads concurrently so a full pipe buffer cannot deadlock the test.
func resolveCapturingStderr(t *testing.T, args Args) (Resolved, string) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	res, rerr := Resolve(args, false)
	_ = w.Close()
	os.Stderr = old
	out := <-done
	if rerr != nil {
		t.Fatalf("Resolve: %v", rerr)
	}
	return res, out
}

// A model swapped out because it belongs to a DIFFERENT provider has to say so.
//
// It was the visible half of the startup failure and the only half with no
// explanation anywhere: the chrome read "(openai) gpt-5" for someone who had
// configured claude-opus-5, and nothing in the output connected the two. The
// sibling repair — an id missing from the catalogue entirely — has warned on
// stderr all along; this branch never did.
func TestModelSwappedForProviderMismatchSaysSo(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	// An explicit --provider, so the credential fallback never runs and this is
	// a pure model repair with no provider switch to fold it into.
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}
	pinConfig(t, "anthropic", "claude-opus-5")

	r, out := resolveCapturingStderr(t, Args{Provider: "openai"})

	if r.Model == "claude-opus-5" {
		t.Fatal("kept an anthropic model on openai; this no longer exercises the repair")
	}
	if !strings.Contains(out, "claude-opus-5") {
		t.Errorf("the warning does not name the model that was dropped:\n%s", out)
	}
	if !strings.Contains(out, r.Model) {
		t.Errorf("the warning does not name the model actually used (%q):\n%s", r.Model, out)
	}
}

// ...but NOT twice. When the provider switch is already being reported, the
// model move is part of that same story — the switch notice names both halves.
// A second line about the model describes one event as two.
func TestAProviderSwitchDoesNotAlsoPrintAModelWarning(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	pinConfig(t, "anthropic", "claude-opus-5")
	if err := config.AuthStoreFor().SetOAuth("anthropic", deadToken()); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}

	r, out := resolveCapturingStderr(t, Args{})

	if r.ProviderSwitch == nil {
		t.Fatal("no provider switch recorded; this no longer exercises the overlap")
	}
	if strings.Contains(out, "belongs to") {
		t.Errorf("printed a model warning alongside a provider switch, describing one event twice:\n%s", out)
	}
}
