package modelreg

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// refCatalog lists one id under two providers, with anthropic first, and an
// OpenRouter-style id that carries a slash whose prefix is also a provider
// that lists the remainder.
func refCatalog() *provider.Registry {
	r := provider.NewRegistry()
	r.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "shared-id"},
		{Provider: "cpa", ID: "shared-id"},
		{Provider: "openrouter", ID: "deepseek/flash"},
		{Provider: "deepseek", ID: "flash"},
		{Provider: "openrouter", ID: "nobody/else"},
	})
	return r
}

func TestResolveRef(t *testing.T) {
	cat := refCatalog()
	for _, tc := range []struct {
		name, ref, prefer string
		wantProv, wantID  string
		warns             bool
	}{
		{"bare id, no preference, takes the first row", "shared-id", "", "anthropic", "shared-id", false},
		{"bare id prefers the current provider", "shared-id", "cpa", "cpa", "shared-id", false},
		{"a preference that does not list the id falls back", "shared-id", "openai", "anthropic", "shared-id", false},
		{"qualified picks the named provider", "cpa/shared-id", "", "cpa", "shared-id", false},
		{"qualified beats the preference", "anthropic/shared-id", "cpa", "anthropic", "shared-id", false},
		{"a split beats a whole id, and says so", "deepseek/flash", "openrouter", "deepseek", "flash", true},
		{"a qualified id with a slash cuts at the first slash", "openrouter/deepseek/flash", "", "openrouter", "deepseek/flash", false},
		{"an id with a slash and no provider prefix stays whole", "nobody/else", "", "openrouter", "nobody/else", false},
		{"surrounding space is ignored", "  cpa/shared-id ", "", "cpa", "shared-id", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, warning, err := resolveRef(cat, tc.ref, tc.prefer)
			if err != nil {
				t.Fatalf("resolveRef(%q, %q): %v", tc.ref, tc.prefer, err)
			}
			if m.Provider != tc.wantProv || m.ID != tc.wantID {
				t.Errorf("resolveRef(%q, %q) = %s/%s, want %s/%s", tc.ref, tc.prefer, m.Provider, m.ID, tc.wantProv, tc.wantID)
			}
			if (warning != "") != tc.warns {
				t.Errorf("resolveRef(%q) warning = %q, want a warning: %v", tc.ref, warning, tc.warns)
			}
			// The warning has to tell the user how to write the other reading.
			if tc.warns && !strings.Contains(warning, "openrouter/deepseek/flash") {
				t.Errorf("warning %q does not name the other reading's qualified form", warning)
			}
		})
	}
	for _, ref := range []string{"", "nope", "cpa/nope", "nope/shared-id", "/shared-id", "cpa/"} {
		if m, _, err := resolveRef(cat, ref, ""); err == nil {
			t.Errorf("resolveRef(%q) = %s/%s, want an error", ref, m.Provider, m.ID)
		}
	}
}

// Every row of the real catalog has a text form that reaches it. This is the
// property split-before-exact exists for: on the built-in catalog, 55
// vercel-ai-gateway ids read as the qualified form of a direct-provider row.
func TestEveryCatalogRowRoundTrips(t *testing.T) {
	cat := provider.NewRegistry()
	rows := cat.Active()
	if len(rows) < 100 {
		t.Fatalf("the built-in catalog has %d rows; this test is not reading it", len(rows))
	}
	for _, m := range rows {
		got, _, err := resolveRef(cat, QualifiedRef(m), "")
		if err != nil {
			t.Errorf("resolveRef(%q): %v", QualifiedRef(m), err)
			continue
		}
		if got.Provider != m.Provider || got.ID != m.ID {
			t.Errorf("QualifiedRef(%s/%s) resolved to %s/%s", m.Provider, m.ID, got.Provider, got.ID)
		}
	}
}
