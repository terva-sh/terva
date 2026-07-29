package agent

import (
	"regexp"
	"strings"
	"testing"
)

// The built-in task board is a first-party feature. Its context card and its
// open-work continuation gate follow the task CONTROLLER — whether a host also
// happens to run extensions has nothing to do with either.
//
// Three hosts had wired them inside `if extMgr != nil`, so a session with no
// extension manager showed the model no task card and never re-prompted on open
// work. Latent, because every host's manager is built unconditionally (under
// --no-ext it is live-but-toolless precisely so the wiring stays uniform) — but
// latent by luck rather than by design, and the nesting reads as though the
// dependency were real.
//
// The shared helper's half is covered by behaviour
// (TestTheTaskBoardDoesNotDependOnExtensions). This guard covers the two hosts
// that wire inline — acp and the daemon — where a unit test would have to stand
// up an editor connection or a whole daemon session to see it.

var extManagerCheck = regexp.MustCompile(`^\s*if\s+extMgr\s*!=\s*nil\s*\{`)

// The card wiring is now one call carrying both layers (build.WireEphemeralTail),
// so the nesting this guards would take the EXTENSION cards down with the task
// card — a strictly worse version of the same mistake, and still invisible.
var taskWiringVerbs = []string{"WireEphemeralTail(", "OpenWorkGate("}

func TestTheTaskBoardIsNotWiredUnderAnExtensionCheck(t *testing.T) {
	var checked int
	for _, f := range census(t) {
		// Brace-track from each `if extMgr != nil {` to its close, and report any
		// task wiring inside. Counting braces on comment-stripped code lines is
		// enough here: these bodies are ordinary statement blocks, and a brace in
		// a string literal would have to appear in one of them to fool it.
		depth := 0
		inside := false
		for _, code := range f.code {
			if !inside && extManagerCheck.MatchString(code) {
				inside, depth = true, 1
				checked++
				continue
			}
			if !inside {
				continue
			}
			depth += strings.Count(code, "{") - strings.Count(code, "}")
			if depth <= 0 {
				inside = false
				continue
			}
			for _, verb := range taskWiringVerbs {
				if strings.Contains(code, verb) {
					t.Errorf("%s wires the task board inside an `if extMgr != nil` block:\n    %s\n"+
						"  The board is not an extension. Nested here, a host with no extension manager shows "+
						"the model no task card and never re-prompts on open work — a first-party feature "+
						"switched off by an unrelated subsystem being absent.", f.path, code)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no `if extMgr != nil` block was found anywhere — the spelling has changed and this guard " +
			"is checking nothing")
	}
}
