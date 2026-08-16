package build

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// The reasoning chain, end to end:
//
//	--reasoning / session  >  models.json per-model  >  global config  >  CATALOG default  >  off
//
// The two model rungs are the same FIELD (Model.DefaultReasoning) on opposite
// sides of the global setting, told apart by DefaultReasoningSet. Getting that
// backwards breaks one of two real users: an operator whose per-model value
// stops applying the moment they touch /settings (the bug this fixes), or a k3
// user whose explicit global level stops applying at all.

// effective runs both halves of the chain the way a turn does: the builder
// resolves the layers it can distinguish, the provider applies the catalog
// default underneath.
func effective(explicit string, m provider.Model, global string) string {
	raw := resolveRawReasoning(explicit, m, global)
	return provider.EffectiveReasoning(provider.NormalizeReasoning(raw), raw != "", m)
}

func TestReasoningPrecedence(t *testing.T) {
	catalogDefault := provider.Model{DefaultReasoning: "high"} // e.g. the k3 rows
	operatorChoice := provider.Model{DefaultReasoning: "low", DefaultReasoningSet: true}
	plain := provider.Model{}

	for _, tc := range []struct {
		name     string
		explicit string
		model    provider.Model
		global   string
		want     string
	}{
		{"nothing set at all", "", plain, "", ""},
		{"global alone", "", plain, "medium", "medium"},
		{"explicit alone", "high", plain, "", "high"},
		{"explicit beats global", "high", plain, "low", "high"},

		// The bug: an operator's per-model value used to vanish here.
		{"operator per-model beats global", "", operatorChoice, "medium", "low"},
		{"operator per-model applies with no global", "", operatorChoice, "", "low"},
		{"explicit still beats operator per-model", "max", operatorChoice, "", "max"},

		// The k3 contract: a CATALOG default is a fallback and must yield.
		{"catalog default applies when nothing chosen", "", catalogDefault, "", "high"},
		{"global beats a catalog default", "", catalogDefault, "low", "low"},
		{"explicit beats a catalog default", "minimum", catalogDefault, "", "minimum"},

		// "off" is an explicit choice that normalizes to "", so it must still
		// suppress every rung below it rather than read as "unset".
		{"an explicit off suppresses a catalog default", "off", catalogDefault, "", ""},
		{"a global off suppresses a catalog default", "", catalogDefault, "off", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effective(tc.explicit, tc.model, tc.global); got != tc.want {
				t.Errorf("explicit=%q perModel=%v(set=%v) global=%q -> %q, want %q",
					tc.explicit, tc.model.DefaultReasoning, tc.model.DefaultReasoningSet,
					tc.global, got, tc.want)
			}
		})
	}
}

// The k3 rows are the live instance of the catalog-default rung, and their
// comment promises the level yields to a user's global choice. Read off the
// real catalog so a row edit that drops DefaultReasoning — or one that starts
// marking it as operator-set — fails here rather than silently downgrading K3
// to K2 in production.
func TestK3CatalogDefaultStillYieldsToAGlobalLevel(t *testing.T) {
	m, err := provider.FindModel("kimi", "k3")
	if err != nil {
		t.Fatalf("kimi/k3 is not in the catalog: %v", err)
	}
	if m.DefaultReasoning == "" {
		t.Fatal("kimi/k3 lost its DefaultReasoning; the endpoint silently downgrades to K2 without one")
	}
	if m.DefaultReasoningSet {
		t.Error("a CATALOG row is marked operator-set; it would now outrank the user's global level")
	}
	if got := effective("", m, ""); got != provider.NormalizeReasoning(m.DefaultReasoning) {
		t.Errorf("k3 with nothing chosen = %q, want its catalog default", got)
	}
	if got := effective("", m, "low"); got != "low" {
		t.Errorf("k3 with a global level = %q, want the user's %q", got, "low")
	}
}

// --thinking is the documented spelling; --reasoning is what shipped. Both must
// parse to the same thing, for the reason /reasoning stayed an alias: an
// accepted flag is never withdrawn, and a script or a habit that predates the
// rename must not start erroring.
func TestBothThinkingFlagSpellingsParse(t *testing.T) {
	for _, flag := range []string{"--thinking", "--reasoning"} {
		a, err := ParseArgs([]string{flag, "high"})
		if err != nil {
			t.Errorf("%s high: %v", flag, err)
			continue
		}
		if a.Reasoning != "high" {
			t.Errorf("%s high stored as %q", flag, a.Reasoning)
		}
	}
	// Teeth: without this the loop cannot fail if validation is dropped
	// entirely, and a guard that cannot fail is not a guard.
	if _, err := ParseArgs([]string{"--thinking", "bogus"}); err == nil {
		t.Error("--thinking bogus was accepted")
	}
}
