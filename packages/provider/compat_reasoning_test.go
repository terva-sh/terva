package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A gateway that fronts a vendor subscription serves the vendor's own model
// ids, and neither /v1/models shape carries a single capability field. Every
// discovered row therefore said "this model does not think", and the request
// builders gate the whole thinking block on exactly that field.
//
// Nothing failed when they got it wrong, which is why it lasted: the backend
// still thought at its own default depth, so the only visible symptom was an
// effort knob that changed nothing.

// modelsServer answers one canned /v1/models page, in whichever shape the
// caller wrote.
func modelsServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func byID(models []Model) map[string]Model {
	out := make(map[string]Model, len(models))
	for _, m := range models {
		out[m.ID] = m
	}
	return out
}

// Discovery borrows what terva already curates for the first-party Claude model
// of the same name, and falls back to the id for a gateway's private alias.
func TestDiscoverAnthropicCompatibleInheritsClaudeThinking(t *testing.T) {
	withCatalogState(t)

	url := modelsServer(t, `{"data":[
		{"id":"claude-opus-5"},
		{"id":"claude-sonnet-4-6"},
		{"id":"claude-fable-5-dd-weiver-otua-xedoc"},
		{"id":"local-llama"}
	],"has_more":false}`)

	got, err := DiscoverAnthropicCompatible(context.Background(), url, "k", 200000, AnthropicCompatOptions{}, testReg.Active())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	m := byID(got)

	// The catalog row is the source of truth, so compare against it rather
	// than against a number that moves whenever Anthropic raises a cap.
	catalog, err := testReg.FindModel("anthropic", "claude-opus-5")
	if err != nil {
		t.Fatalf("no first-party claude-opus-5 row to inherit from: %v", err)
	}
	opus := m["claude-opus-5"]
	if !opus.Reasoning {
		t.Error("claude-opus-5: Reasoning false, so buildRequest sends no thinking block at all")
	}
	if !opus.AdaptiveThinking {
		t.Error("claude-opus-5: AdaptiveThinking false, so the request carries a budget this family rejects")
	}
	if opus.MaxOutput != catalog.MaxOutput {
		t.Errorf("claude-opus-5: MaxOutput = %d, want the catalog's %d", opus.MaxOutput, catalog.MaxOutput)
	}
	if !opus.Has(CapReasoning) {
		t.Error("claude-opus-5: Has(CapReasoning) false, so the picker calls it incapable of thinking")
	}

	// Sonnet 4.6 thinks but is NOT adaptive-only. Inheriting the flag wholesale
	// is the point: a blanket "every Claude is adaptive" rule would get this
	// row wrong and send the shape the model does not take.
	sonnet := m["claude-sonnet-4-6"]
	if !sonnet.Reasoning {
		t.Error("claude-sonnet-4-6: Reasoning false")
	}
	if sonnet.AdaptiveThinking {
		t.Error("claude-sonnet-4-6: AdaptiveThinking true, but this row takes an explicit budget")
	}

	// A gateway alias with no catalog row falls back to the id. Every Fable
	// model shipped so far is adaptive-only.
	alias := m["claude-fable-5-dd-weiver-otua-xedoc"]
	if !alias.Reasoning || !alias.AdaptiveThinking {
		t.Errorf("gateway alias: Reasoning=%v AdaptiveThinking=%v, want both true from the id",
			alias.Reasoning, alias.AdaptiveThinking)
	}

	// A false hit is the expensive direction: it sends a thinking block to a
	// model that has none, and the API rejects every turn.
	if m["local-llama"].Reasoning {
		t.Error("local-llama: Reasoning true, but nothing about that id says it thinks")
	}
}

// The remedy itself, on the wire. A test that asserted Model.Reasoning would
// pass with the request wiring deleted.
func TestAnthropicCompatRequestCarriesTheEffortKnob(t *testing.T) {
	withCatalogState(t)

	t.Run("a reasoning row sends thinking and an effort", func(t *testing.T) {
		testReg.SetUserModels([]Model{{
			Provider: "my-gateway", ID: "claude-opus-5",
			ContextWindow: 200000, MaxOutput: 32768,
			Reasoning: true, AdaptiveThinking: true,
		}})
		t.Cleanup(func() { testReg.SetUserModels(nil) })

		_, body := anthCompatCapture(t, "k", AnthropicCompatOptions{}, Request{
			Model:        "claude-opus-5",
			Reasoning:    "high",
			ReasoningSet: true,
		})

		thinking, ok := body["thinking"].(map[string]any)
		if !ok {
			raw, _ := json.Marshal(body)
			t.Fatalf("no thinking block on the wire: %s", raw)
		}
		if thinking["type"] != "adaptive" {
			t.Errorf("thinking.type = %v, want adaptive for this family", thinking["type"])
		}
		cfg, ok := body["output_config"].(map[string]any)
		if !ok {
			t.Fatal("no output_config, so the effort level never reaches the model")
		}
		if effort, _ := cfg["effort"].(string); effort == "" {
			t.Error("output_config.effort is empty, so /reasoning high does nothing")
		}
	})

	// The id fallback, for a row that reached terva without the flag: a proxy
	// catalog, or a models.json entry that can set reasoning but has no field
	// for the thinking MODE. Opus 5 rejects an explicit budget, so reading the
	// id is what keeps that row off the shape the model refuses.
	t.Run("the id decides the mode when the flag is absent", func(t *testing.T) {
		testReg.SetUserModels([]Model{{
			Provider: "my-gateway", ID: "claude-opus-5",
			ContextWindow: 200000, MaxOutput: 32768,
			Reasoning: true, AdaptiveThinking: false,
		}})
		t.Cleanup(func() { testReg.SetUserModels(nil) })

		_, body := anthCompatCapture(t, "k", AnthropicCompatOptions{}, Request{
			Model:        "claude-opus-5",
			Reasoning:    "high",
			ReasoningSet: true,
		})
		thinking, ok := body["thinking"].(map[string]any)
		if !ok {
			t.Fatal("no thinking block on the wire")
		}
		if thinking["type"] != "adaptive" {
			t.Errorf("thinking.type = %v, want adaptive from the id", thinking["type"])
		}
		if _, budgeted := thinking["budget_tokens"]; budgeted {
			t.Error("an explicit budget rode along, which this family rejects")
		}
	})

	// The negative control. Without it the test above would pass against code
	// that sends a thinking block unconditionally.
	t.Run("a non-reasoning row sends none", func(t *testing.T) {
		testReg.SetUserModels([]Model{{
			Provider: "my-gateway", ID: "plain-model",
			ContextWindow: 200000, MaxOutput: 8192,
		}})
		t.Cleanup(func() { testReg.SetUserModels(nil) })

		_, body := anthCompatCapture(t, "k", AnthropicCompatOptions{}, Request{
			Model:        "plain-model",
			Reasoning:    "high",
			ReasoningSet: true,
		})
		if _, present := body["thinking"]; present {
			t.Error("a model with no reasoning capability was sent a thinking block")
		}
	})
}

// The OpenAI-compatible twin. Its lookup is cross-provider on purpose, because
// one gateway fronts several vendors at once.
func TestDiscoverOpenAICompatibleInheritsReasoning(t *testing.T) {
	withCatalogState(t)

	url := modelsServer(t, `{"data":[
		{"id":"gpt-5.6-sol"},
		{"id":"gemma-4-12b-it"}
	]}`)

	got, err := DiscoverOpenAICompatible(context.Background(), url, "k", 200000, testReg.Active())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	m := byID(got)

	if !m["gpt-5.6-sol"].Reasoning {
		t.Error("gpt-5.6-sol: Reasoning false, so the request carries no reasoning_effort")
	}
	if !m["gpt-5.6-sol"].Has(CapReasoning) {
		t.Error("gpt-5.6-sol: Has(CapReasoning) false")
	}
	if m["gemma-4-12b-it"].Reasoning {
		t.Error("gemma-4-12b-it: Reasoning true, but no catalog row says so")
	}
}

// 🪤 Discovery must not read its own previous output.
//
// Active() holds the live layer, so a row this endpoint discovered LAST time
// is a candidate for the id lookup THIS time. A stale row saying "does not
// think" therefore re-stamps itself on every rediscovery and the curated
// answer can never win. Anyone whose models cache predates the capability
// inheritance is in exactly that state, so the repair would never reach them.
//
// Found by the terva-review on PR #1300, finding-1 (medium).
func TestOpenAICompatCapsIgnoresAStaleRowFromTheEndpointItself(t *testing.T) {
	withCatalogState(t)

	const id = "gpt-5.6-sol"
	curated, err := testReg.FindModel("openai-codex", id)
	if err != nil || !curated.Reasoning {
		t.Skipf("no curated reasoning row for %q to prefer", id)
	}

	// The endpoint's own row from a previous discovery, before the capability
	// inheritance existed. This is what a real stale models-cache.json holds,
	// BaseURL included: DiscoverOpenAICompatible stamps one on every row it
	// writes, and that is the mark openAICompatCaps uses to skip its own output.
	testReg.SetLiveModels([]Model{{
		Provider: "cpa-openai", ID: id,
		ContextWindow: 200000, Reasoning: false,
		BaseURL: "https://cpa-api.example.invalid",
		Source:  "live",
	}})
	t.Cleanup(func() { testReg.SetLiveModels(nil) })

	if got := openAICompatCaps(testReg.Active(), id); !got.Reasoning {
		t.Error("discovery inherited its own stale row, so the endpoint stays " +
			"reasoning-incapable forever and the curated answer never wins")
	}
}

// When providers disagree about an id, the answer is "no evidence", never the
// row that happened to sort first.
//
// This is not hypothetical. gemini-2.5-pro and grok-code-fast-1 each appear
// under two providers in the shipped catalog with opposite Reasoning flags, so
// a gateway serving either one had its capability decided by catalog position.
// The rows here are synthetic so the test keeps its meaning after somebody
// reconciles those two.
//
// Found by the terva-review on PR #1300, finding-1 (medium).
func TestOpenAICompatCapsRefusesToGuessWhenProvidersDisagree(t *testing.T) {
	withCatalogState(t)
	const id = "collide-me"

	t.Run("unanimous rows are evidence", func(t *testing.T) {
		testReg.SetLiveModels([]Model{
			{Provider: "prov-a", ID: id, Reasoning: true, Source: "live"},
			{Provider: "prov-b", ID: id, Reasoning: true, Source: "live"},
		})
		t.Cleanup(func() { testReg.SetLiveModels(nil) })
		if !openAICompatCaps(testReg.Active(), id).Reasoning {
			t.Error("two rows agreed the model reasons and the answer was no")
		}
	})

	t.Run("disagreeing rows are not", func(t *testing.T) {
		testReg.SetLiveModels([]Model{
			{Provider: "prov-a", ID: id, Reasoning: true, Source: "live"},
			{Provider: "prov-b", ID: id, Reasoning: false, Source: "live"},
		})
		t.Cleanup(func() { testReg.SetLiveModels(nil) })
		if openAICompatCaps(testReg.Active(), id).Reasoning {
			t.Error("the providers disagree, so this stamped reasoning_effort on " +
				"an id another row calls incapable; the backend rejects that turn")
		}
	})

	// The effort vocabulary gets its own vote, and it loses differently. Rows
	// can agree a model thinks and still name different rungs, and the first
	// row's private extension must not ride out on catalog position.
	//
	// Found by the second terva-review on PR #1300, finding-1 (medium).
	t.Run("a disagreeing effort vocabulary is dropped, not the reasoning flag", func(t *testing.T) {
		testReg.SetLiveModels([]Model{
			{Provider: "prov-a", ID: id, Reasoning: true,
				ReasoningEfforts: []string{"low", "high", "xhigh"}, Source: "live"},
			{Provider: "prov-b", ID: id, Reasoning: true,
				ReasoningEfforts: []string{"low", "high"}, Source: "live"},
		})
		t.Cleanup(func() { testReg.SetLiveModels(nil) })

		got := openAICompatCaps(testReg.Active(), id)
		if !got.Reasoning {
			t.Error("both rows agreed the model reasons, so that answer stands; " +
				"an argument about the rungs must not void the agreement")
		}
		if len(got.ReasoningEfforts) != 0 {
			t.Errorf("kept %v from whichever row sorted first, so discovery "+
				"advertises a rung only one provider declared", got.ReasoningEfforts)
		}
	})

	// The mirror of the case above. clampEffortToDeclared reads the list into a
	// map, so a different order is the same enum, and calling it a disagreement
	// would throw away a vocabulary both rows in fact agreed on.
	t.Run("order alone is not disagreement", func(t *testing.T) {
		testReg.SetLiveModels([]Model{
			{Provider: "prov-a", ID: id, Reasoning: true,
				ReasoningEfforts: []string{"low", "high"}, Source: "live"},
			{Provider: "prov-b", ID: id, Reasoning: true,
				ReasoningEfforts: []string{"high", "low"}, Source: "live"},
		})
		t.Cleanup(func() { testReg.SetLiveModels(nil) })

		if got := openAICompatCaps(testReg.Active(), id); len(got.ReasoningEfforts) != 2 {
			t.Errorf("got %v, want the two rungs both rows declared", got.ReasoningEfforts)
		}
	})
}

// buildRequest asks this client's own provider first. The bare cross-provider
// lookup it replaced handed one provider's row to another's request, which is
// how an endpoint serving gpt-5.6-sol silently acquired openai-codex's prices
// and context window along with its reasoning flag.
func TestOpenAIBuildRequestPrefersItsOwnProviderRow(t *testing.T) {
	withCatalogState(t)

	const id = "gpt-5.6-sol"
	testReg.SetUserModels([]Model{{
		Provider: "my-gateway", ID: id,
		ContextWindow: 200000, MaxOutput: 8192,
		Reasoning: false,
	}})
	t.Cleanup(func() { testReg.SetUserModels(nil) })

	// The control: the id also exists elsewhere, and that row DOES think. If
	// the scoped lookup regressed to the bare one, this is the row it finds.
	if other, err := testReg.FindModel("", id); err != nil || !other.Reasoning {
		t.Skipf("no reasoning row for %q under another provider; nothing to prefer over", id)
	}

	c := &openaiClient{catalogRef: catalogRef{testReg}, name: "my-gateway"}
	out, err := c.buildRequest(Request{
		Model:        id,
		Reasoning:    "high",
		ReasoningSet: true,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if out.ReasoningEffort != "" {
		t.Errorf("reasoning_effort = %q, but this endpoint's own row says the model does not think; "+
			"the request borrowed another provider's row", out.ReasoningEffort)
	}
}

// The fallback keeps lending the thinking claim, and that is deliberate. The
// third terva-review on PR #1300 asked for it to be stripped. Stripping it
// turns TestOpenAICompatAnthropicReasoningEffort and the openai-compatible arm
// of TestReasoningEffectMatchesWhatTheBuilderSends red, because a gateway that
// fronts another vendor has no row of its own and the claim can only come from
// that vendor's row. The reasoning sits beside the fallback in openai.go.

// A gateway serving GPT-6 ids inherits OpenAI's declared effort sets from the
// catalog, and with them the two things the blind mapper gets wrong: off must
// send "none" on Sol and Luna (their Chat Completions function calling only
// works there), and the top rungs must reach xhigh and max rather than stop
// at "high". Astra declares no "none", so off keeps omitting the field.
//
// Each id has two catalog rows (openai-codex and openai-responses), and
// discovery drops the list if they disagree, so this also fails if a later
// edit changes one row's set and not the other's.
func TestDiscoverOpenAICompatibleInheritsGPT6Efforts(t *testing.T) {
	withCatalogState(t)

	url := modelsServer(t, `{"data":[
		{"id":"gpt-6-sol"},
		{"id":"gpt-6-luna"},
		{"id":"gpt-6-astra"},
		{"id":"gpt-6.1-sol"}
	]}`)
	got, err := DiscoverOpenAICompatible(context.Background(), url, "k", 200000, testReg.Active())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	m := byID(got)

	for _, tc := range []struct {
		id   string
		want map[string]string // terva level -> reasoning_effort on the wire
	}{
		{"gpt-6-sol", map[string]string{"off": "none", "minimum": "low", "low": "low", "medium": "medium", "high": "high", "maximum": "xhigh", "max": "max"}},
		{"gpt-6-luna", map[string]string{"off": "none", "minimum": "low", "low": "low", "medium": "medium", "high": "high", "maximum": "xhigh", "max": "max"}},
		{"gpt-6-astra", map[string]string{"off": "", "minimum": "low", "low": "low", "medium": "medium", "high": "high", "maximum": "xhigh", "max": "max"}},
		{"gpt-6.1-sol", map[string]string{"off": "", "minimum": "low", "low": "low", "medium": "medium", "high": "high", "maximum": "xhigh", "max": "max"}},
	} {
		row := m[tc.id]
		if len(row.ReasoningEfforts) == 0 {
			t.Errorf("%s: no efforts inherited; the catalog rows disagree or declare none", tc.id)
			continue
		}
		for lv, want := range tc.want {
			if got := openAICompatEffort(row, lv); got != want {
				t.Errorf("%s at %q sends reasoning_effort %q, want %q", tc.id, lv, got, want)
			}
			// The ladder and the wire must describe the same request.
			if eff := ReasoningEffectFor(row, lv); eff.Effort != want {
				t.Errorf("%s at %q: ReasoningEffectFor says %q, wire sends %q", tc.id, lv, eff.Effort, want)
			}
		}
	}
}
