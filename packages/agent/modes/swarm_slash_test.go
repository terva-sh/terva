package modes

import (
	"context"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
)

// newInteractiveForSwarmTest builds the minimal Interactive scaffolding
// runSwarm needs. It does NOT call NewInteractive (which would pull in
// the whole TUI); the runSwarm method only touches cfg.Swarm, the
// status mutex, and the swarm dialog, so we hand-build those.
func newInteractiveForSwarmTest(t *testing.T) (*Interactive, *swarm.Swarm) {
	t.Helper()
	root := t.TempDir()
	f := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error {
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
	iv := &Interactive{
		turns:       newTurnEngine(),
		swarmDialog: newSwarmDialog(),
		dirty:       make(chan struct{}, 1),
	}
	iv.cfg.Swarm = f
	return iv, f
}

// TestRunSwarmBareDoesNotPanic regression-tests the slice-out-of-range
// panic that hit when /swarm was typed with no subcommand: runSwarm
// did args[1:] without checking len(args), which panics as [1:0].
func TestRunSwarmBareDoesNotPanic(t *testing.T) {
	iv, _ := newInteractiveForSwarmTest(t)
	defer iv.cfg.Swarm.StopAll()

	// Bare /swarm: parts[1:] from the dispatcher is an empty slice.
	iv.runSwarm(context.Background(), nil)

	if !iv.swarmDialog.Active() {
		t.Fatal("bare /swarm should open the dashboard")
	}
}

func TestRunSwarmSubcommandsDoNotPanic(t *testing.T) {
	iv, _ := newInteractiveForSwarmTest(t)
	defer iv.cfg.Swarm.StopAll()

	// Each row is the slice that the dispatcher hands to runSwarm —
	// i.e. parts[1:] where parts was strings.Fields of the slash
	// command. Mixing zero-arg and arg'd forms exercises both
	// branches of the reslice guard.
	cases := [][]string{
		{"list"},
		{"new"},
		{"new", "fix", "the", "thing"},
		{"kill"},
		{"kill", "no-such-id"},
		{"remove"},
		{"remove", "no-such-id"},
		{"send"},
		{"send", "no-such-id"},
		{"send", "no-such-id", "hello", "world"},
		{"bogus"},
	}
	for _, args := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("runSwarm(%v) panicked: %v", args, r)
				}
			}()
			iv.runSwarm(context.Background(), args)
		}()
	}
}

func TestRunSwarmNewSpawnsAgent(t *testing.T) {
	iv, f := newInteractiveForSwarmTest(t)
	defer f.StopAll()

	iv.runSwarm(context.Background(), []string{"new", "do", "stuff"})
	agents := f.List()
	if len(agents) != 1 {
		t.Fatalf("want 1 agent; got %d", len(agents))
	}
	if agents[0].Task != "do stuff" {
		t.Fatalf("task = %q; want %q", agents[0].Task, "do stuff")
	}
}

// TestRunSwarmSendDeliversToAgentInbox spins up a real agent with a
// fake Runner whose only job is to forward inbox lines to a channel,
// then asserts the /swarm send <id> <text...> path routes through
// Swarm.SendUserTurn and lands at the agent verbatim.
func TestRunSwarmSendDeliversToAgentInbox(t *testing.T) {
	root := t.TempDir()
	recv := make(chan string, 4)
	f := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error {
				// Stand up a real Listener on the agent's inbox path so
				// SendUserTurn (which dials a unix socket) actually has
				// something to talk to. The runner-test stubs do the
				// same; this is the minimum to exercise the wire.
				ln, err := swarm.Listen(a.InboxPath)
				if err != nil {
					return err
				}
				defer ln.Close()
				for {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case line, ok := <-ln.Lines():
						if !ok {
							return nil
						}
						recv <- line
					}
				}
			})
		},
	})
	defer f.StopAll()
	iv := &Interactive{swarmDialog: newSwarmDialog(), dirty: make(chan struct{}, 1), turns: newTurnEngine()}
	iv.cfg.Swarm = f

	a, err := f.Spawn(context.Background(), "do thing")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Run /swarm send <id> <text...>. The dispatcher would have
	// already strings.Fields-ed the input; mirror that here.
	iv.runSwarm(context.Background(), []string{"send", a.ID, "please", "continue"})

	select {
	case msg := <-recv:
		// The wire is now a newline-safe JSON envelope; decode it
		// rather than asserting the raw bytes.
		kind, text := swarm.ParseInboxLine(msg)
		if kind != "user" || text != "please continue" {
			t.Fatalf("agent received %q → (%q, %q); want (user, please continue)", msg, kind, text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent to receive the prompt")
	}

	if iv.statusErr != "" {
		t.Fatalf("status err set: %q", iv.statusErr)
	}
	if iv.statusOK == "" || iv.statusOK[:7] != "sent to" {
		t.Fatalf("status ok = %q; want \"sent to ...\"", iv.statusOK)
	}
}

func TestParseSpawnFlags(t *testing.T) {
	cases := []struct {
		in                  string
		wantModel, wantProv string
		wantTask            string
	}{
		{"do x", "", "", "do x"},
		{"--model claude do x", "claude", "", "do x"},
		{"--model=claude do x", "claude", "", "do x"},
		{"--provider openai --model gpt-5 do x", "gpt-5", "openai", "do x"},
		{"--provider=openai --model=gpt-5 do x", "gpt-5", "openai", "do x"},
		// Only LEADING flags are consumed.
		{"do --model x", "", "", "do --model x"},
		// Missing value: --model with no follow-up token leaves model empty
		// and the next field starts the task.
		{"--model", "", "", ""},
	}
	for _, c := range cases {
		m, p, task := parseSpawnFlags(c.in)
		if m != c.wantModel || p != c.wantProv || task != c.wantTask {
			t.Errorf("parseSpawnFlags(%q) = (%q,%q,%q); want (%q,%q,%q)",
				c.in, m, p, task, c.wantModel, c.wantProv, c.wantTask)
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
		swarmDialog: newSwarmDialog(),
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
