package provider

import "testing"

// ReasoningLadderFor is now the single builder both frontends read through, so
// it must not become a second opinion about what a rung does. Every rung of
// every catalog model has to match ReasoningEffectFor, which is what the
// request builders themselves delegate to.
//
// Scanned from the catalog rather than a typed-out list of models: the first
// run IS the audit, and a model added later enrolls itself.
//
// 🪤 Catalog, NOT builtinCatalog. This scanned builtinCatalog, the third-party
// EXTENDED list, so every CURATED row in models.go went unaudited — 322 rungs
// across 46 models, measured, and openai-codex / kimi / deepseek entirely,
// since those three have no builtinCatalog rows at all. (anthropic, openai and
// google do have rows there, so they were partly covered; the file header's
// "not duplicated here" describes intent more than the data.) Catalog is the
// union — models.go plus builtinCatalog, appended in catalog_builtin.go's init.
//
// The same hole in reasoning_wire_census_test.go is what let kimi's
// misclassification survive: that guard dedupes by provider, and kimi had zero
// rows to dedupe from, so it was never examined at all.
func TestLadderNeverDisagreesWithTheEffectMappers(t *testing.T) {
	checked := 0
	seen := map[string]bool{}
	for _, m := range Catalog {
		seen[m.Provider] = true
		ladder := ReasoningLadderFor(m)
		if ladder == nil {
			// nil is a claim in its own right: NO rung may be supported.
			for _, lv := range ReasoningLevels {
				if ReasoningEffectFor(m, LadderWireValue(lv)).Supported {
					t.Fatalf("%s/%s: ladder is nil but rung %q is supported",
						m.Provider, m.ID, lv)
				}
			}
			continue
		}
		if len(ladder) != len(ReasoningLevels) {
			t.Fatalf("%s/%s: ladder has %d rungs, the ladder has %d",
				m.Provider, m.ID, len(ladder), len(ReasoningLevels))
		}
		for i, rung := range ladder {
			if rung.Level != ReasoningLevels[i] {
				t.Fatalf("%s/%s: rung %d is %q, want %q (ladder order is load-bearing)",
					m.Provider, m.ID, i, rung.Level, ReasoningLevels[i])
			}
			want := ReasoningEffectFor(m, LadderWireValue(rung.Level))
			if rung.Effect != want {
				t.Errorf("%s/%s rung %q: ladder says %+v, the mappers say %+v",
					m.Provider, m.ID, rung.Level, rung.Effect, want)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no rungs checked — the catalog scan found nothing, so this guard is vacuous")
	}

	// 🪤 checked > 0 is NOT enough to prove this scanned the right slice. The
	// builtinCatalog version checked 2786 rungs — a large, reassuring number
	// that cleared that floor while auditing no curated row at all. So name the
	// providers that exist ONLY in the curated list: if the scan ever narrows
	// back, their absence says so instead of a big number saying nothing.
	//
	// Deliberately not anthropic/openai/google, which also have builtinCatalog
	// rows and so would stay "seen" through exactly the regression this catches.
	for _, p := range []string{"openai-codex", "kimi", "deepseek"} {
		if !seen[p] {
			t.Errorf("provider %q was not scanned. It lives only in the curated Catalog rows, "+
				"so this guard is reading builtinCatalog (the third-party extended list) and "+
				"auditing none of the providers most worth auditing.", p)
		}
	}
	t.Logf("checked %d rungs across %d providers", checked, len(seen))
}

// The collapse annotation has to be self-consistent or a picker built on it
// lies twice: a rung that points at another must land on the SAME wire value as
// the rung it names, and the rung it names must itself be unannotated.
// Otherwise "same as low" could point at a rung that is itself "same as
// minimum", and neither row would say what it sends.
// Catalog, not builtinCatalog — same reason as the guard above.
func TestCollapseAnnotationsPointAtARealCanonicalRung(t *testing.T) {
	for _, m := range Catalog {
		ladder := ReasoningLadderFor(m)
		byLevel := map[string]ReasoningRung{}
		for _, r := range ladder {
			byLevel[r.Level] = r
		}
		for _, r := range ladder {
			if r.SameAs == "" {
				continue
			}
			target, ok := byLevel[r.SameAs]
			if !ok {
				t.Errorf("%s/%s rung %q points at %q, which is not a rung",
					m.Provider, m.ID, r.Level, r.SameAs)
				continue
			}
			if target.Effect != r.Effect {
				t.Errorf("%s/%s rung %q claims to be the same as %q, but sends %+v vs %+v",
					m.Provider, m.ID, r.Level, r.SameAs, r.Effect, target.Effect)
			}
			if target.SameAs != "" {
				t.Errorf("%s/%s rung %q points at %q, which is itself a duplicate of %q",
					m.Provider, m.ID, r.Level, r.SameAs, target.SameAs)
			}
		}
	}
}

// The teeth for the pair above: a model whose rungs genuinely collapse must
// actually be annotated. Without this, deleting the SameAs computation
// altogether would leave both guards green.
//
// 🪤 This named gemini-3-pro-preview and t.Skipf'd when it was missing. The
// catalog renamed that row to gemini-3.1-pro-preview, so the guard with the
// teeth had been SKIPPING — passing by not running, which is the one outcome a
// guard must never have. A named fixture in a catalog that turns over is a
// scheduled skip.
//
// So it enrolls itself: find every model whose ladder genuinely sends one wire
// value for several rungs, and require each to be annotated. It cannot skip, it
// cannot rot on a rename, and a catalog with no collapsing model at all fails
// loudly rather than passing on an empty set.
func TestACollapsingModelIsActuallyAnnotated(t *testing.T) {
	collapsing := 0
	for _, m := range Catalog {
		ladder := ReasoningLadderFor(m)
		if ladder == nil {
			continue
		}
		// Collapse means two rungs landing on the same wire value — under the
		// SAME exclusions the builder applies, or this becomes the second
		// opinion the file exists to prevent. ReasoningLadderFor never
		// annotates an Off() rung: several rungs sending nothing is the normal
		// shape of a low ladder, and "same as off" would be noise, not
		// information.
		firstAt := map[ReasoningEffect]string{}
		collapsed := ""
		for _, r := range ladder {
			if !r.Effect.Supported || r.Effect.Off() {
				continue
			}
			if prev, dup := firstAt[r.Effect]; dup {
				collapsed = prev + "/" + r.Level
				break
			}
			firstAt[r.Effect] = r.Level
		}
		if collapsed == "" {
			continue
		}
		collapsing++

		annotated := false
		for _, r := range ladder {
			if r.SameAs != "" {
				annotated = true
				break
			}
		}
		if !annotated {
			t.Errorf("%s/%s: rungs %s send the same wire value, but no rung is annotated — "+
				"a picker built on this ladder shows two rows that look different and are not",
				m.Provider, m.ID, collapsed)
		}
	}
	if collapsing == 0 {
		t.Fatal("no model in the catalog collapses any rung, so this guard proved nothing. " +
			"Either the ladder mappers stopped collapsing (unlikely) or the scan is reading " +
			"the wrong slice")
	}
	t.Logf("checked %d models whose rungs collapse", collapsing)
}
