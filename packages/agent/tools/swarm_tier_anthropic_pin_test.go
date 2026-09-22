package tools

import "testing"

// The anthropic rungs name a model, and this says which one.
//
// Every other guard in swarm_tier_census_test.go reads the TABLE and checks it
// against the catalog, which is the right shape for auditing a rule set — but
// it means the table can change what it resolves to and every one of them still
// passes. That is not hypothetical. The medium and strong rungs sat on
// claude-opus-4-1 ($15/$75) for as long as the only claude-opus-5 row was
// speculative, because a speculative row is skipped and the bare "opus" caught
// 4.1 first. Adding a live claude-opus-5-5 row moved both rungs to it in the
// same commit that added the model — a better pick, decided by nobody, and the
// full suite stayed green either way.
//
// What a `tier: strong` swarm spawn or a raati seat dispatches to is a
// deliberate choice about cost and capability. So one test states the answer
// outright: the next time it moves, someone has to come here and say so.
//
// This is the test to EDIT, not to delete, when the pin advances. Update the
// pin list in swarmTierFamilies and the constant here together; a diff that
// touches both is the decision being recorded.
func TestAnthropicTierRungsResolveToTheirPin(t *testing.T) {
	const (
		wantWeak   = "claude-haiku-4-5"
		wantMedium = "claude-opus-5-5"
		wantStrong = "claude-opus-5-5"
	)

	for _, tc := range []struct {
		rung, want string
	}{
		{"weak", wantWeak},
		{"medium", wantMedium},
		{"strong", wantStrong},
	} {
		fam, listed := swarmTierFamilies["anthropic"][tc.rung]
		if !listed {
			t.Errorf("anthropic has no %s rung; the ladder is meant to be complete "+
				"for this provider (raati rigor level 1 seats one member per rung)", tc.rung)
			continue
		}
		if got := fam.resolve("anthropic"); got != tc.want {
			t.Errorf("anthropic/%s resolves to %q, want %q.\n"+
				"If this is a deliberate move, update the pin list in swarmTierFamilies "+
				"and the constant in this test together. If it is not, a catalog row "+
				"changed what a %s sub-agent gets dispatched to without anyone choosing it.",
				tc.rung, got, tc.want, tc.rung)
		}
	}

	// The pin has to sit above every BROADER entry, or it does nothing: the
	// match list is walked in order, so an earlier keyword that also covers
	// the pinned model reaches the catalog first and the pin is never
	// consulted.
	//
	// "Broader" is asked of the production matcher rather than hardcoded, and
	// the question is the one that matters: does this earlier entry itself
	// match the pinned id? An entry that does is a superset of the pin and
	// shadows it; an entry that does not — a dated snapshot, say — is
	// narrower and may legitimately sit above it.
	//
	// Checking only the bare "opus" was not enough, and the first version of
	// this test shipped that way. The list also holds "claude-opus-5", which
	// covers every Opus 5.x by substring: reordered to {"claude-opus-5",
	// "claude-opus-5-5", "opus"} the resolved-id assertion above still passes,
	// because 5.5 happens to be the first matching row today — and the rung
	// silently moves the next time an Opus 5.x lands. Found in review of
	// #1316, reproduced by that reordering.
	for _, rung := range []string{"medium", "strong"} {
		match := swarmTierFamilies["anthropic"][rung].match
		pin := -1
		for i, m := range match {
			if m == wantStrong {
				pin = i
				break
			}
		}
		if pin < 0 {
			t.Errorf("anthropic/%s no longer pins a generation (%v); it is back to "+
				"resolving on catalog order", rung, match)
			continue
		}
		for i, earlier := range match[:pin] {
			if (tierFamily{match: []string{earlier}}).matches(wantStrong) {
				t.Errorf("anthropic/%s lists %q at %d, which already matches the pinned "+
					"%q at %d.\nThe match list is walked in order, so the broader entry "+
					"reaches the catalog first and the pin is never consulted — the rung "+
					"resolves on catalog order again, which is what pinning exists to stop.",
					rung, earlier, i, wantStrong, pin)
			}
		}
	}

	// The degrade path is the other half of the pattern the google rows use:
	// a catalog that has dropped every pinned generation should still reach
	// some Opus rather than leaving the rung dead.
	for _, rung := range []string{"medium", "strong"} {
		match := swarmTierFamilies["anthropic"][rung].match
		if len(match) == 0 || match[len(match)-1] != "opus" {
			t.Errorf("anthropic/%s does not end in the bare %q (%v). Without it, a "+
				"catalog whose pinned generations have all retired has no rung at all.",
				rung, "opus", match)
		}
	}
}
