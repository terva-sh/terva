package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/build"
)

// Every stem a machine flow resolves by literal name must be enrolled in
// build.MachineBoundPersonaStems — the self-enrolling-set pattern. The set is
// what makes the silent-shadow announcement (MachinePersonaNotice) and any
// future save-time warning complete: a new machine-bound stem added here
// without joining the set fails this test instead of shipping unannounced.
func TestMachineBoundStemsEnrolled(t *testing.T) {
	for _, stem := range []string{doctorPersona, editorPersona, dramaturgPersona, realizePersona} {
		if !build.MachineBoundPersonaStems[stem] {
			t.Errorf("machine flow resolves persona %q by stem, but it is not in build.MachineBoundPersonaStems", stem)
		}
	}
	if got, want := len(build.MachineBoundPersonaStems), 4; got != want {
		t.Errorf("MachineBoundPersonaStems has %d entries, the flows use %d — a stem was enrolled without a flow or a flow's stem renamed", got, want)
	}
}
