package modes

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/modes/dialogs"
)

// newInteractiveForSwarmTest builds the minimal scaffolding runSwarm needs on
// the carrier path — the only path there is. It does NOT call NewInteractive
// (which would pull in the whole TUI): runSwarm touches the carrier, the status
// mutex and the swarm dialog, so those are hand-built.
//
// This used to stand up a real in-process *swarm.Swarm and assign cfg.Swarm.
// That field was the direct driver's, nil under every frontend once the driver
// went away, so these tests were exercising an arm production could not reach.
// The behavioural assertions they carried live on the live path already —
// TestCarrierSwarmDashboard covers spawn/stop/send/resume through SurfaceAction,
// TestCarrierSwarmDisabled covers the unavailable case, and the foreign-backend
// gate moved to worker.TestAllowSpawn* beside the gate itself. What is kept here
// is what only this layer can break: runSwarm's own argument parsing.
func newInteractiveForSwarmTest(t *testing.T) *Interactive {
	t.Helper()
	iv := newCtrlprotoTestInteractive()
	iv.cfg.Carrier = newFakeCarrier()
	iv.cfg.CarrierTasks = true
	iv.swarmDialog = dialogs.NewSwarmDialog()
	return iv
}

// TestRunSwarmBareDoesNotPanic regression-tests the slice-out-of-range panic
// that hit when /swarm was typed with no subcommand: runSwarm did args[1:]
// without checking len(args), which panics as [1:0].
func TestRunSwarmBareDoesNotPanic(t *testing.T) {
	iv := newInteractiveForSwarmTest(t)

	// Bare /swarm: parts[1:] from the dispatcher is an empty slice.
	iv.runSwarm(context.Background(), nil)

	if !iv.swarmDialog.Active() {
		t.Fatal("bare /swarm should open the dashboard")
	}
}

func TestRunSwarmSubcommandsDoNotPanic(t *testing.T) {
	iv := newInteractiveForSwarmTest(t)

	// Each row is the slice the dispatcher hands to runSwarm — i.e. parts[1:]
	// where parts was strings.Fields of the slash command. Mixing zero-arg and
	// arg'd forms exercises both branches of every subcommand's parsing.
	for _, args := range [][]string{
		{"new"},
		{"new", "do", "stuff"},
		{"stop"},
		{"stop", "nonexistent-id"},
		{"remove"},
		{"remove", "nonexistent-id"},
		{"send"},
		{"send", "nonexistent-id"},
		{"send", "nonexistent-id", "hello", "there"},
		{"resume"},
		{"resume", "nonexistent-id"},
		{"nonsense-subcommand"},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("/swarm %v panicked: %v", args, r)
				}
			}()
			iv.runSwarm(context.Background(), args)
		}()
	}
}

func TestParseSpawnFlags(t *testing.T) {
	cases := []struct {
		in                                            string
		wantModel, wantProv, wantPersona, wantBackend string
		wantTask                                      string
	}{
		{"do x", "", "", "", "", "do x"},
		{"--model claude do x", "claude", "", "", "", "do x"},
		{"--model=claude do x", "claude", "", "", "", "do x"},
		{"--provider openai --model gpt-5 do x", "gpt-5", "openai", "", "", "do x"},
		{"--provider=openai --model=gpt-5 do x", "gpt-5", "openai", "", "", "do x"},
		{"--persona vartija review x", "", "", "vartija", "", "review x"},
		{"--persona=vartija review x", "", "", "vartija", "", "review x"},
		{"--persona ./d.md --model=gpt-5 t", "gpt-5", "", "./d.md", "", "t"},
		// --backend, both forms, and combined with other flags.
		{"--backend claude do x", "", "", "", "claude", "do x"},
		{"--backend=terva:portable do x", "", "", "", "terva:portable", "do x"},
		{"--backend claude --model=gpt-5 t", "gpt-5", "", "", "claude", "t"},
		// Only LEADING flags are consumed.
		{"do --model x", "", "", "", "", "do --model x"},
		{"do --persona x", "", "", "", "", "do --persona x"},
		{"do --backend x", "", "", "", "", "do --backend x"},
		// Missing value: --model with no follow-up token leaves model empty
		// and the next field starts the task.
		{"--model", "", "", "", "", ""},
	}
	for _, c := range cases {
		m, p, persona, backend, task := parseSpawnFlags(c.in)
		if m != c.wantModel || p != c.wantProv || persona != c.wantPersona || backend != c.wantBackend || task != c.wantTask {
			t.Errorf("parseSpawnFlags(%q) = (%q,%q,%q,%q,%q); want (%q,%q,%q,%q,%q)",
				c.in, m, p, persona, backend, task, c.wantModel, c.wantProv, c.wantPersona, c.wantBackend, c.wantTask)
		}
	}
}

func TestSplitIDAndRest(t *testing.T) {
	cases := []struct {
		in       string
		wantID   string
		wantText string
	}{
		{"", "", ""},
		{"  ", "", ""},
		{"alpha", "alpha", ""},
		{"alpha hello world", "alpha", "hello world"},
		{"  alpha   hello   world  ", "alpha", "hello   world  "},
		{"alpha\thi", "alpha", "hi"},
	}
	for _, c := range cases {
		gotID, gotText := splitIDAndRest(c.in)
		if gotID != c.wantID || gotText != c.wantText {
			t.Errorf("splitIDAndRest(%q) = (%q,%q); want (%q,%q)", c.in, gotID, gotText, c.wantID, c.wantText)
		}
	}
}

func TestRunSwarmWithoutSwarmIsNoop(t *testing.T) {
	iv := &Interactive{
		turns:       newTurnEngine(),
		swarmDialog: dialogs.NewSwarmDialog(),
		dirty:       make(chan struct{}, 1),
	}
	// cfg.Swarm stays nil. The command should set a status err and
	// otherwise be inert.
	iv.runSwarm(context.Background(), nil)
	if iv.swarmDialog.Active() {
		t.Fatal("dialog opened despite no swarm")
	}
	if iv.statusErr == "" {
		t.Fatal("expected a status error when swarm is nil")
	}
}
