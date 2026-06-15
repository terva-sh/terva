//go:build terva_acp

package agent

import (
	"testing"

	"terva.sh/terva/packages/agent/acp"
	"terva.sh/terva/packages/provider"
)

func hasModelOption(opts []acp.ModelOption, prov, id string) bool {
	for _, o := range opts {
		if o.Provider == prov && o.ID == id {
			return true
		}
	}
	return false
}

// Regression: the ACP model menu must INCLUDE speculative models (an entire
// provider's catalog can be speculative — openai-codex today — so excluding
// them hides the provider from the editor). Gating is on authentication only.
func TestModelOptionsForIncludesSpeculative(t *testing.T) {
	models := []provider.Model{
		{Provider: "openai-codex", ID: "gpt-5.4", DisplayName: "GPT-5.4", Speculative: true},
		{Provider: "deepseek", ID: "deepseek-chat", DisplayName: "DeepSeek"},
		{Provider: "anthropic", ID: "claude", DisplayName: "Claude"}, // not authed
	}
	authed := map[string]bool{"openai-codex": true, "deepseek": true}

	got := modelOptionsFor(models, authed)

	if !hasModelOption(got, "openai-codex", "gpt-5.4") {
		t.Errorf("speculative openai-codex model was excluded from the menu: %+v", got)
	}
	if !hasModelOption(got, "deepseek", "deepseek-chat") {
		t.Errorf("authenticated deepseek model missing: %+v", got)
	}
	if hasModelOption(got, "anthropic", "claude") {
		t.Errorf("unauthenticated anthropic model should be excluded: %+v", got)
	}
}

// A model with no DisplayName falls back to its id as the label.
func TestModelOptionsForLabelFallback(t *testing.T) {
	got := modelOptionsFor(
		[]provider.Model{{Provider: "deepseek", ID: "deepseek-chat"}},
		map[string]bool{"deepseek": true},
	)
	if len(got) != 1 || got[0].DisplayName != "deepseek-chat" {
		t.Errorf("label fallback to id failed: %+v", got)
	}
}
