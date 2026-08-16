package provider

import (
	"strings"
	"testing"
)

// kimi is Kimi CODE: Kimi behind the Anthropic Messages API. The registry builds
// it with NewKimiCodingWithHeaders in every auth mode, so its requests carry an
// Anthropic thinking BUDGET. terva had it recorded as OpenAI-compatible in two
// independent places, and both said the same wrong thing — /reasoning reported
// an effort enum, and --dump-prompt=wire serialized an OpenAI chat body.
//
// 🪤 These assert AGREEMENT WITH THE WIRE rather than the contents of
// reasoningWireWiring. A test that checked the map would pass on a build where
// the routing is right and the budget still disagrees, which is most of what
// could go wrong here — and it would restate the fix instead of checking it.

// kimiReasoningModel resolves a reasoning-capable kimi model from the catalog.
func kimiReasoningModel(t *testing.T) Model {
	t.Helper()
	withCatalogState(t)
	ResetCatalogLayers()
	m, err := FindModel("kimi", "k3")
	if err != nil {
		t.Fatalf("kimi k3 is not in the catalog: %v", err)
	}
	if !m.Reasoning {
		t.Fatal("kimi k3 is no longer a reasoning model; this guard needs a new subject")
	}
	return m
}

// What /reasoning prints must be what the request carries. Before the fix these
// disagreed completely: ReasoningEffectFor said {Budget:0, Effort:"high"} while
// buildRequest sent thinking{enabled, budget_tokens:16384} — a dialog reporting
// a knob the provider does not accept, and no budget at all.
func TestKimiReasoningEffectMatchesTheRequestItSends(t *testing.T) {
	m := kimiReasoningModel(t)

	for _, level := range []string{"low", "medium", "high", "maximum"} {
		t.Run(level, func(t *testing.T) {
			eff := ReasoningEffectFor(m, level)
			if !eff.Supported {
				t.Fatalf("reasoning reported unsupported for a reasoning model at %q", level)
			}

			wire, err := (&anthropicClient{name: "kimi"}).buildRequest(Request{
				Model:        m.ID,
				Reasoning:    level,
				ReasoningSet: true,
				Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if wire.Thinking == nil {
				t.Fatalf("the request carries no thinking block at %q, so there is nothing for the dialog to describe", level)
			}

			if eff.Budget != wire.Thinking.BudgetTokens {
				t.Errorf("/reasoning would report budget %d; the request carries %d",
					eff.Budget, wire.Thinking.BudgetTokens)
			}
			// The other half of the old bug: an effort string is the OpenAI
			// knob, and this wire has no field to put it in.
			if eff.Effort != "" {
				t.Errorf("reported effort %q, but kimi sends a thinking budget and no effort field", eff.Effort)
			}
		})
	}
}

// The dump has to serialize the body terva would actually send. Asserting on the
// Anthropic-shaped field rather than on inequality with the OpenAI shape, so
// this cannot pass on some third wrong body.
func TestKimiWireDumpIsAnthropicShaped(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	body, field, err := wireBody("kimi", "apikey", Request{
		Model:    "k3",
		System:   "you are a test",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if field != "messages" {
		t.Errorf("input field = %q, want messages", field)
	}
	if _, ok := body.(*anthRequest); !ok {
		t.Fatalf("kimi dumped a %T; it speaks the Anthropic Messages API", body)
	}
}

// 🪤 kimi authenticates with x-api-key even when the credential is a
// subscription token — NewKimiCodingSourceWithHeaders leaves the client in
// api-key mode — so an "oauth" auth method must NOT pull in Anthropic's
// subscription framing. Honoring the mode here would put Claude Code's identity
// prompt in a kimi dump, which is the same lie the OpenAI arm used to tell,
// pointing the other way.
func TestKimiWireDumpIgnoresOAuthFraming(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	req := Request{
		Model:    "k3",
		System:   "you are a test",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
		Tools:    []Tool{{Name: "read", Description: "read a file"}},
	}
	for _, method := range []string{"apikey", "oauth", ""} {
		out, err := DumpRequestJSONL("kimi", method, req)
		if err != nil {
			t.Fatalf("auth method %q: %v", method, err)
		}
		if got := string(out); strings.Contains(got, claudeCodeIdentity) {
			t.Errorf("auth method %q put Anthropic's identity block in a kimi dump", method)
		}
		if got := string(out); strings.Contains(got, `"name":"Read"`) {
			t.Errorf("auth method %q applied Anthropic's tool renaming to kimi", method)
		}
	}
}
