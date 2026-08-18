package worker

import (
	"os/exec"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// okBackend is a spec that satisfies every contract, so a case below can break
// exactly one thing and the refusal names that thing rather than the first
// omission it happens to hit.
func okBackend(name string) Backend {
	return Backend{
		Name:      name,
		Command:   func(Dispatch) (*exec.Cmd, error) { return exec.Command("true"), nil },
		Translate: func([]byte) []Event { return nil },
	}
}

// TestRegisterRefusesAHalfWiredBackend: every one of these specs is accepted by
// the compiler and produces a worker that HANGS — a task that never arrives, an
// approval nothing answers, a tool gated twice. None of them fails in a way
// anyone can read, which is why the contract belongs where the spec is built
// rather than in a paragraph above it.
func TestRegisterRefusesAHalfWiredBackend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  Backend
		wants string
	}{
		{
			name:  "no Command",
			spec:  Backend{Name: "x", Translate: func([]byte) []Event { return nil }},
			wants: "no Command",
		},
		{
			name:  "no Translate",
			spec:  Backend{Name: "x", Command: func(Dispatch) (*exec.Cmd, error) { return nil, nil }},
			wants: "no Translate",
		},
		{
			name: "Opening without Steer",
			spec: func() Backend {
				b := okBackend("x")
				b.Opening = func(Briefing) string { return "do the thing" }
				return b
			}(),
			wants: "Opening but not Steer",
		},
		{
			name: "RecognizeAsk without EncodeApprove",
			spec: func() Backend {
				b := okBackend("x")
				b.RecognizeAsk = func(Event) (Ask, bool) { return Ask{}, false }
				return b
			}(),
			wants: "cannot answer them",
		},
		{
			name: "both approval carriers",
			spec: func() Backend {
				b := okBackend("x")
				b.ApprovalSocket = true
				b.RecognizeAsk = func(Event) (Ask, bool) { return Ask{}, false }
				b.EncodeApprove = func(string, core.ConfirmDecision) ([]byte, error) { return nil, nil }
				return b
			}(),
			wants: "never both",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.validate()
			if err == nil {
				t.Fatalf("validate accepted a spec with %s — Register would take it, and the failure "+
					"would surface as a worker that hangs", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("refusal = %q, want it to name %q — a panic at startup is only useful if it "+
					"says which rule broke", err, tc.wants)
			}
		})
	}

	if err := okBackend("x").validate(); err != nil {
		t.Errorf("a complete spec was refused (%v) — every case above is now passing for the wrong reason", err)
	}
}

// TestRegisterPanicsRatherThanShippingIt: validate is the readable half; this is
// the half that actually stops it. Backends register from init(), so the panic
// fails the binary at startup instead of one dispatch, months later.
func TestRegisterPanicsRatherThanShippingIt(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register accepted a backend with no Command")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "no Command") {
			t.Errorf("panic = %v, want it to name the missing field", r)
		}
	}()
	Register(Backend{Name: "half-wired-probe", Translate: func([]byte) []Event { return nil }})
}

// TestEveryRegisteredBackendHonoursItsSpec walks the REGISTRY rather than a list
// of names, so a backend added tomorrow is held to the same rules without anyone
// remembering this test exists.
//
// Register already refuses these at startup, which makes this the belt to that
// braces — but it is the half that keeps working if the validation is ever
// loosened, and it is where the frame contracts (which Register must not check)
// live.
func TestEveryRegisteredBackendHonoursItsSpec(t *testing.T) {
	names := Names()
	if len(names) < 3 {
		t.Fatalf("found %d registered backend(s) — the registry is not populated: %v", len(names), names)
	}
	for _, name := range names {
		b, err := Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if err := b.validate(); err != nil {
			t.Errorf("registered backend %q does not honour its own spec: %v", name, err)
		}
	}
}

// TestEveryBackendFramesItsLines is the contract that was not even prose.
//
// writeStdin writes a frame verbatim and every worker reads its stdin a line at
// a time, so a frame with no trailing newline either parks the worker mid-line
// or merges with whatever is written next. Both halves of this bit while a fake
// backend was being written for the ask-carrier test: the opening turn and an
// approval arrived as one unparseable line, and before that the worker sat on a
// reply that never completed.
//
// Checked here rather than in Register because checking it means CALLING these,
// and Register runs at package init.
func TestEveryBackendFramesItsLines(t *testing.T) {
	checked := 0
	for _, name := range Names() {
		b, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if b.Steer != nil {
			frame, err := b.Steer("a follow-up turn")
			if err != nil {
				t.Errorf("%s: Steer returned an error on ordinary text: %v", name, err)
			} else if !strings.HasSuffix(string(frame), "\n") {
				t.Errorf("%s: Steer frame does not end in a newline (%q) — the worker reads lines, so "+
					"this one never completes and the next frame merges into it", name, frame)
			}
			checked++
		}
		if b.EncodeApprove != nil {
			frame, err := b.EncodeApprove("ask-1", core.ConfirmDecision{Allow: true})
			if err != nil {
				t.Errorf("%s: EncodeApprove returned an error on an ordinary decision: %v", name, err)
			} else if !strings.HasSuffix(string(frame), "\n") {
				t.Errorf("%s: EncodeApprove frame does not end in a newline (%q) — a gated worker would "+
					"sit on a reply it cannot read to the end", name, frame)
			}
			checked++
		}
	}
	if checked < 4 {
		t.Fatalf("only %d frame encoder(s) exercised — the registry's backends have stopped carrying "+
			"Steer/EncodeApprove, and this test is passing by checking nothing", checked)
	}
}
