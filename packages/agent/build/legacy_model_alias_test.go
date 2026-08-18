package build

import (
	"sort"
	"testing"

	"terva.sh/terva/packages/provider"
)

// TestLegacyModelAliasesAreDead checks that every key in models.json's
// legacy-alias table is a name the provider registry no longer knows.
//
// The table exists to keep pre-rename config files working. That is only safe
// while each key is DEAD. The moment a key is also a live registry id or alias,
// the same string means two different providers depending on which code path
// reads it, and the models.json path wins silently — no warning, no error.
//
// Both failures this catches were live:
//
//   - "openai-responses" was mapped to "openai" while being a first-class
//     registry id with its own client, catalog rows, reasoning wire and label.
//     An override keyed "openai-responses" landed on "openai" instead, and
//     since the write side does not normalize, /model displayed it as saved
//     while the merged catalog carried it on the other provider. baseUrl and
//     prices travel that path too.
//
//   - "moonshot" was mapped to "kimi" while the registry makes it an alias of
//     "moonshotai" — a different provider on a different wire, as
//     provider/reasoning.go says in so many words.
//
// A key that is a live ALIAS is allowed only if it resolves to the same
// provider the registry resolves it to; otherwise the two tables disagree.
func TestLegacyModelAliasesAreDead(t *testing.T) {
	canonical := map[string]bool{}
	aliasOf := map[string]string{}
	for _, spec := range providerSpecs {
		canonical[spec.id] = true
		for _, a := range spec.aliases {
			if _, taken := aliasOf[a]; !taken {
				aliasOf[a] = spec.id
			}
		}
	}

	keys := make([]string, 0, len(provider.LegacyUserModelProviderAliases))
	for k := range provider.LegacyUserModelProviderAliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		to := provider.LegacyUserModelProviderAliases[key]

		if canonical[key] {
			t.Errorf("legacy alias %q is a CANONICAL provider id, but models.json rewrites it to %q.\n"+
				"  An operator who writes a %q block gets an override on %q instead, with no warning — and "+
				"because the write side does not normalize, /model shows it as saved while the catalog "+
				"carries it elsewhere. Delete the entry.", key, to, key, to)
			continue
		}
		if target, isAlias := aliasOf[key]; isAlias && target != to {
			t.Errorf("legacy alias %q is a live registry alias of %q, but models.json rewrites it to %q.\n"+
				"  The same string names two different providers depending on which table reads it. "+
				"Delete the entry and let the registry resolve it.", key, target, to)
			continue
		}
		// The destination must itself be a real provider, or the rewrite sends
		// the override into a provider that does not exist.
		if !canonical[to] {
			if _, isAlias := aliasOf[to]; !isAlias {
				t.Errorf("legacy alias %q rewrites to %q, which is not a registry id or alias", key, to)
			}
		}
	}
}

// TestLegacyModelAliasGuardHasTeeth pins that the check above compares against
// the real registry. A green result is meaningless if providerSpecs were empty
// or the alias table unreadable — the two conditions under which every
// assertion above passes vacuously.
func TestLegacyModelAliasGuardHasTeeth(t *testing.T) {
	if len(providerSpecs) < 10 {
		t.Fatalf("providerSpecs has %d entries; the alias guard is passing vacuously", len(providerSpecs))
	}
	if len(provider.LegacyUserModelProviderAliases) == 0 {
		t.Fatal("the legacy alias table is empty; the guard above asserts nothing")
	}

	// The two names that were wrong must NOT be in the table any more, and both
	// must still be reachable through the registry under their real meaning.
	for _, gone := range []string{"openai-responses", "moonshot"} {
		if to, ok := provider.LegacyUserModelProviderAliases[gone]; ok {
			t.Errorf("%q is back in the legacy alias table (-> %q); it is a live registry name and "+
				"rewriting it silently retargets an operator's override", gone, to)
		}
	}
	if got := provider.NormalizeUserModelProviderKey("openai-responses"); got != "openai-responses" {
		t.Errorf("NormalizeUserModelProviderKey(openai-responses) = %q, want it left alone", got)
	}
	if got := provider.NormalizeUserModelProviderKey("moonshot"); got != "moonshot" {
		t.Errorf("NormalizeUserModelProviderKey(moonshot) = %q, want it left alone", got)
	}
	// A genuinely dead alias must still be rewritten, or the table stopped working.
	if got := provider.NormalizeUserModelProviderKey("anthropic-messages"); got != "anthropic" {
		t.Errorf("NormalizeUserModelProviderKey(anthropic-messages) = %q, want %q", got, "anthropic")
	}
}
