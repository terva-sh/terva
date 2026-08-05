package provider

import "testing"

// ReasoningLadderFor is now the single builder both frontends read through, so
// it must not become a second opinion about what a rung does. Every rung of
// every catalog model has to match ReasoningEffectFor, which is what the
// request builders themselves delegate to.
//
// Scanned from the catalog rather than a typed-out list of models: the first
// run IS the audit, and a model added later enrolls itself.
func TestLadderNeverDisagreesWithTheEffectMappers(t *testing.T) {
	checked := 0
	for _, m := range builtinCatalog {
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
	t.Logf("checked %d rungs", checked)
}

// The collapse annotation has to be self-consistent or a picker built on it
// lies twice: a rung that points at another must land on the SAME wire value as
// the rung it names, and the rung it names must itself be unannotated.
// Otherwise "same as low" could point at a rung that is itself "same as
// minimum", and neither row would say what it sends.
func TestCollapseAnnotationsPointAtARealCanonicalRung(t *testing.T) {
	for _, m := range builtinCatalog {
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
func TestACollapsingModelIsActuallyAnnotated(t *testing.T) {
	m, err := FindModel("google", "gemini-3-pro-preview")
	if err != nil {
		t.Skipf("fixture model missing: %v", err)
	}
	annotated := 0
	for _, r := range ReasoningLadderFor(m) {
		if r.SameAs != "" {
			annotated++
		}
	}
	// gemini-3 lands minimum on low and medium/maximum/max on high.
	if annotated < 2 {
		t.Errorf("gemini-3-pro-preview has %d collapsed rungs; it sends one enum for "+
			"several rungs, so the annotation is missing", annotated)
	}
}
