package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// A ModelInfo.Ladder key is meaningless without the table it indexes, so the
// one thing a client must be able to rely on is that every key it is handed
// resolves. A dangling key would render as "this model takes no thinking
// setting" — a confident wrong answer, not a visible failure.
//
// Scanned over the whole catalog rather than a fixture list, so a model added
// later is covered without anyone remembering to add it.
func TestEveryLadderKeyResolves(t *testing.T) {
	tbl := newLadderTable()
	keyed, bare := 0, 0
	for _, m := range provider.Active() {
		k := tbl.keyFor(m)
		if k == "" {
			bare++
			continue
		}
		keyed++
	}
	table := tbl.table()
	// Re-walk after the table is complete: keyFor interns as it goes, so a key
	// handed out early must still resolve once every model has been seen.
	for _, m := range provider.Active() {
		k := tbl.keyFor(m)
		if k == "" {
			continue
		}
		rows, ok := table[k]
		if !ok {
			t.Fatalf("%s/%s carries ladder key %q, which is not in the table",
				m.Provider, m.ID, k)
		}
		if len(rows) != len(provider.ReasoningLevels) {
			t.Fatalf("%s/%s ladder %q has %d rows, want %d",
				m.Provider, m.ID, k, len(rows), len(provider.ReasoningLevels))
		}
	}
	if keyed == 0 {
		t.Fatal("no model got a ladder — this guard is vacuous")
	}
	t.Logf("%d models keyed into %d ladders, %d take no thinking setting",
		keyed, len(table), bare)
}

// The dedup is the whole reason the table exists: inlining the rows on every
// ModelInfo added 94 KB to a 99 KB models.list. If a future change made the
// identity accidentally per-model (folding in the id, say), the wire would
// quietly double and every guard above would still pass.
func TestLaddersActuallyDeduplicate(t *testing.T) {
	tbl := newLadderTable()
	keyed := 0
	for _, m := range provider.Active() {
		if tbl.keyFor(m) != "" {
			keyed++
		}
	}
	distinct := len(tbl.table())
	if distinct == 0 || keyed == 0 {
		t.Fatal("nothing to deduplicate — vacuous")
	}
	// The real numbers are 440 models across 11 ladders. A generous ceiling
	// catches "the identity became per-model" without pinning a count that
	// every catalog addition would have to update.
	if distinct > keyed/4 {
		t.Errorf("%d models produced %d distinct ladders — the identity is too "+
			"specific and the table has stopped deduplicating", keyed, distinct)
	}
	t.Logf("%d models -> %d distinct ladders", keyed, distinct)
}

// Two models that send the same thing must share an entry, and two that do not
// must not — the failure that would merge Codex's "high" with Gemini's "HIGH"
// and describe one model with the other's wire values.
func TestLadderIdentityTracksTheWireNotTheModel(t *testing.T) {
	codex, err := provider.FindModel("openai-codex", "gpt-5.6-sol")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	gem, err := provider.FindModel("google", "gemini-3-pro-preview")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	tbl := newLadderTable()
	if a, b := tbl.keyFor(codex), tbl.keyFor(gem); a == b {
		t.Errorf("codex and gemini-3 share ladder key %q, but one sends effort "+
			"%q where the other sends %q", a,
			provider.ReasoningEffectFor(codex, "high").Effort,
			provider.ReasoningEffectFor(gem, "high").Effort)
	}
	// ...and asking twice is stable, or a client's key would change under it
	// mid-response.
	if a, b := tbl.keyFor(codex), tbl.keyFor(codex); a != b {
		t.Errorf("the same model got two keys: %q then %q", a, b)
	}
}

// A model that accepts no reasoning control gets NO key, which a client renders
// as "takes no thinking setting". Handing it a ladder of all-off rungs would
// say something different and wrong: that the user has switched thinking off.
func TestModelsWithNoReasoningControlGetNoKey(t *testing.T) {
	var subject provider.Model
	for _, m := range provider.Active() {
		if m.Reasoning && !provider.ReasoningEffectFor(m, "high").Supported {
			subject = m
			break
		}
	}
	if subject.ID == "" {
		t.Skip("no reasoning-flagged model routes to a wire that takes no control")
	}
	if k := newLadderTable().keyFor(subject); k != "" {
		t.Errorf("%s/%s takes no thinking setting but was given ladder key %q",
			subject.Provider, subject.ID, k)
	}
}

// The wire rows must carry the CLAMPED budget the builder sends, not the
// ladder constant — the same bug the TUI dialog had, arriving by a new route.
func TestWireRowsCarryTheClampedBudget(t *testing.T) {
	m, err := provider.FindModel("anthropic", "claude-opus-4-1-20250805")
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	tbl := newLadderTable()
	// Intern BEFORE reading the table: Go evaluates the index expression's
	// operands left to right, so tbl.table()[tbl.keyFor(m)] would look the key
	// up in a table that keyFor had not yet filled.
	key := tbl.keyFor(m)
	rows := tbl.table()[key]
	var maximum ctrlproto.ReasoningRungInfo
	for _, r := range rows {
		if r.Level == "maximum" {
			maximum = r
		}
	}
	if maximum.Budget == 0 {
		t.Fatal("fixture no longer sends a budget — pick another budget-wire model")
	}
	if want := provider.ReasoningBudget("maximum"); maximum.Budget == want {
		t.Errorf("maximum carries the unclamped ladder constant %d; this model's "+
			"output cap forces it lower", want)
	}
}
