package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func statusModelRow(t *testing.T, f statusFacts) string {
	t.Helper()
	for _, row := range statusRows(tui.Theme{}, f) {
		plain := widgets.StripANSIBytes(row)
		if strings.Contains(plain, "model") {
			return plain
		}
	}
	t.Fatal("no model row in /status")
	return ""
}

// /status names the model AND its id when the operator renamed it. Never one
// instead of the other: /status is the view you open to find out what you are
// actually talking to, and a nickname alone cannot answer that.
func TestStatusShowsNameAndID(t *testing.T) {
	row := statusModelRow(t, statusFacts{
		Provider:  "ollama",
		Model:     "hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL",
		ModelName: "Qwen Coder",
	})
	if !strings.Contains(row, "Qwen Coder") {
		t.Errorf("/status dropped the operator's name: %q", row)
	}
	if !strings.Contains(row, "hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL") {
		t.Errorf("/status dropped the id — the fact it exists to report: %q", row)
	}
	if !strings.Contains(row, "ollama /") {
		t.Errorf("/status dropped the provider: %q", row)
	}
}

// An un-renamed model reads exactly as it did before names existed.
func TestStatusUnchangedWithoutAName(t *testing.T) {
	row := statusModelRow(t, statusFacts{Provider: "anthropic", Model: "claude-sonnet-4-5"})
	if !strings.Contains(row, "anthropic / claude-sonnet-4-5") {
		t.Errorf("unrenamed /status row changed shape: %q", row)
	}
	if strings.Contains(row, "(") {
		t.Errorf("no name means no parenthetical: %q", row)
	}
}

// The status bar is the one surface that swaps the name IN for the id — it has
// a line's worth of room and the id can be longer than the whole bar. Model.Label
// is the shared rule; assert it directly, including that a catalog display
// name does NOT qualify (they run longer than the ids they'd replace).
func TestModelLabelPrefersOnlyOperatorNames(t *testing.T) {
	renamed := provider.Model{ID: "hf.co/unsloth/very-long:Q4", DisplayName: "Qwen", DisplayNameSet: true}
	if got := renamed.Label(); got != "Qwen" {
		t.Errorf("renamed label = %q", got)
	}
	catalog := provider.Model{ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5 (latest)"}
	if got := catalog.Label(); got != "claude-sonnet-4-5" {
		t.Errorf("a catalog name must not displace the id in the status bar, got %q", got)
	}
	bare := provider.Model{ID: "llama3"}
	if got := bare.Label(); got != "llama3" {
		t.Errorf("bare label = %q", got)
	}
	// A flag set with an empty name must not blank the bar.
	empty := provider.Model{ID: "llama3", DisplayNameSet: true}
	if got := empty.Label(); got != "llama3" {
		t.Errorf("empty name should fall back to the id, got %q", got)
	}
}
