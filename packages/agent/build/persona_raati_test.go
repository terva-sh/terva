package build

import (
	"strings"
	"testing"
)

// TestRaatiCrewChartersCarryBallotContract pins the ballot contract on every
// shipped raati-crew charter (the deliberation twin of the review crew's
// deliverable contract): the coordinator reads only the fenced ```ballot
// block as the unit's vote, and units must not drift toward the panel between
// rounds. Losing either sentence quietly breaks tallying or reintroduces the
// sycophancy the blind round exists to prevent — see raati-crew/README.md and
// docs/proposals/raati-deliberation.md.
func TestRaatiCrewChartersCarryBallotContract(t *testing.T) {
	markers := []string{
		"every reply ends with your current ballot",
		"never to close the gap with the panel",
	}
	dir := BuiltinPersonasRoot + "/raati-crew"
	entries, err := BuiltinPersonasFS.ReadDir(dir)
	if err != nil {
		t.Fatalf("read embedded raati-crew dir: %v", err)
	}
	charters := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		charters++
		raw, err := BuiltinPersonasFS.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, marker := range markers {
			if !strings.Contains(string(raw), marker) {
				t.Errorf("%s is missing the ballot contract (marker %q)", name, marker)
			}
		}
		p, err := ParsePersona(string(raw), "embedded:raati-crew/"+name)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		// Panelists are convened as a whole panel by the raati coordinator; an
		// empty good_for keeps them out of the swarm_spawn dispatch roster,
		// where a lone panelist would be an opinion, not a tally.
		if len(p.GoodFor) != 0 {
			t.Errorf("%s has non-empty good_for %v; raati panelists must not be singly dispatchable", name, p.GoodFor)
		}
	}
	if charters != 3 {
		t.Fatalf("expected the three-unit raati crew, found %d charters", charters)
	}
}
