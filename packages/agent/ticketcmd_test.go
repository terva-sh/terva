package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gtcli "github.com/terva-sh/git-ticket/cli"
	gtview "github.com/terva-sh/git-ticket/tui/view"

	"terva.sh/terva/packages/testsupport"
)

// The delegation contract of slice 1 (docs/plans/git-ticket.md): terva
// runs git-ticket's embedded command surface and returns its documented
// exit statuses. The store rides --store here so the test does not
// depend on Git-root discovery from a temp directory.
func TestTicketDelegationExitStatuses(t *testing.T) {
	ticketActorHome(t, "")
	tmp := testsupport.TempDir(t)
	store := filepath.Join(tmp, ".tickets")
	var out, errB bytes.Buffer

	if code := runTicketIn(tmp, []string{"--store", store, "init"}, &out, &errB); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	out.Reset()
	errB.Reset()

	if code := runTicketIn(tmp, []string{"--store", store, "list"}, &out, &errB); code != 0 {
		t.Fatalf("list: exit %d, stderr %q", code, errB.String())
	}
	out.Reset()
	errB.Reset()

	code := runTicketIn(tmp, []string{"--store", store, "show", "TKT-NOPE"}, &out, &errB)
	if code == 0 {
		t.Fatal("show of a missing ticket exited 0; git-ticket's nonzero status was lost")
	}
	if !strings.Contains(errB.String(), "ticket_not_found") {
		t.Errorf("stderr lacks git-ticket's own error code: %q", errB.String())
	}
}

// Only the exact `ticket` token dispatches. The plural was superseded by
// git-ticket's plan section 2, and an argv that starts with anything
// else must fall through to terva's normal parser.
func TestTicketCommandDispatch(t *testing.T) {
	if handled, _ := runTicketCommand(nil); handled {
		t.Fatal("empty argv dispatched")
	}
	if handled, _ := runTicketCommand([]string{"tickets"}); handled {
		t.Fatal("the superseded plural dispatched")
	}
	if handled, _ := runTicketCommand([]string{"--help"}); handled {
		t.Fatal("a flag dispatched")
	}
}

func TestExitCodeErrorMessage(t *testing.T) {
	if got := (ExitCodeError{Code: 3}).Error(); got != "exit status 3" {
		t.Fatalf("ExitCodeError message: %q", got)
	}
}

// ticketStore initializes a store under a temp directory and returns its
// path, which the tests ride through --store rather than depending on
// Git-root discovery from a temp directory.
func ticketStore(t *testing.T) (dir, store string) {
	t.Helper()
	// Pinned so the init below writes the empty roster these tests rewrite.
	// A remembered actor on the machine would seed one instead.
	ticketActorHome(t, "")
	dir = testsupport.TempDir(t)
	store = filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer
	if code := runTicketIn(dir, []string{"--store", store, "init"}, &out, &errB); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	return dir, store
}

// git-ticket's cli package reaches a terminal only through Env.RunUI, and
// terva left that field nil, so `terva ticket ui` refused with "no
// terminal UI wired". The control case pins that refusal to the nil
// binding. Without it a passing treatment case would prove nothing: the
// string could have been absent for any reason.
func TestTicketUIRefusalTracksTheTerminalBinding(t *testing.T) {
	dir, store := ticketStore(t)
	var out, errB bytes.Buffer

	// Control: the nil binding, which is what shipped.
	code := runTicketInWithUI(dir, []string{"--store", store, "ui"}, &out, &errB, nil)
	if code == 0 {
		t.Fatal("ui with no terminal binding exited 0")
	}
	if !strings.Contains(errB.String(), "no terminal UI wired") {
		t.Fatalf("the nil binding did not produce the wiring refusal: %q", errB.String())
	}

	// Treatment: a binding is reached, and the refusal is gone.
	out.Reset()
	errB.Reset()
	calls := 0
	runUI := func(p gtcli.UIParams) error {
		calls++
		if p.Store == nil {
			t.Error("the UI was handed a nil store")
		}
		return nil
	}
	if code := runTicketInWithUI(dir, []string{"--store", store, "ui"}, &out, &errB, runUI); code != 0 {
		t.Fatalf("ui: exit %d, stderr %q", code, errB.String())
	}
	if calls != 1 {
		t.Fatalf("the terminal binding ran %d times, want 1", calls)
	}
	if strings.Contains(errB.String(), "no terminal UI wired") {
		t.Errorf("the wiring refusal survived a wired binding: %q", errB.String())
	}
}

// runTicketProcUI must actually reach view.RunProcStore, which is the
// half the fake binding above cannot check. Only RunProcStore produces
// the terminal refusal, so that message is the evidence.
//
// stdin is pointed at /dev/null for the call. That makes the refusal
// certain rather than a bet on how `go test` wires the test binary, and
// it means this test can never open an alternate-screen application in
// the terminal a developer ran it from.
func TestTicketProcUIRefusesWithoutATerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	saved := os.Stdin
	os.Stdin = devNull
	defer func() { os.Stdin = saved }()

	err = runTicketProcUI(gtcli.UIParams{})
	if err == nil {
		t.Fatal("the process terminal binding accepted a stdin that is not a terminal")
	}
	if !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("error does not name the terminal requirement, so the binding may not reach RunProcStore: %v", err)
	}
}

// A person driving the ticket UI must not sign the agent's name to their
// edits. cli resolves the actor before the handoff, and with no --actor
// it hands over the empty actor, which the store resolves to the first
// entry in config.yml. This walks that whole path and reads back what
// landed on disk.
func TestTicketUIWritesRecordTheHumanActor(t *testing.T) {
	dir, store := ticketStore(t)

	// A roster shaped like this repository's: the human first, the agent
	// second. `init` writes an empty roster, and an empty roster refuses
	// a write outright instead of choosing anybody.
	cfgPath := filepath.Join(store, "config.yml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.yml: %v", err)
	}
	roster := "actors:\n  - id: human:sothr\n    name: \"\"\n  - id: agent:terva/mieli\n    name: \"\"\n"
	updated := strings.Replace(string(cfg), "actors: []\n", roster, 1)
	if updated == string(cfg) {
		t.Fatalf("config.yml no longer has the empty roster this test rewrites: %q", cfg)
	}
	if err := os.WriteFile(cfgPath, []byte(updated), 0o644); err != nil {
		t.Fatalf("write config.yml: %v", err)
	}

	var params gtcli.UIParams
	var out, errB bytes.Buffer
	runUI := func(p gtcli.UIParams) error {
		params = p
		return nil
	}
	if code := runTicketInWithUI(dir, []string{"--store", store, "ui"}, &out, &errB, runUI); code != 0 {
		t.Fatalf("ui: exit %d, stderr %q", code, errB.String())
	}

	// The write path the UI itself takes, rather than a reimplementation
	// of it that could drift from the real one.
	id, err := gtview.StoreActions(gtview.StoreParams(params)).Create("filed from the ticket UI", "", "")
	if err != nil {
		t.Fatalf("create through the UI actions: %v", err)
	}
	tk, err := params.Store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	if tk.CreatedBy == nil {
		t.Fatal("the created ticket records no actor")
	}
	if tk.CreatedBy.ID != "human:sothr" {
		t.Errorf("the ticket UI recorded %q, want human:sothr; a person driving the UI must not sign the agent's name", tk.CreatedBy.ID)
	}
}

// `terva ticket init` with no --actor creates a store that refuses every
// write, and git-ticket's own closing advice is to go and create a
// ticket, which is the very thing that fails. The warning is the only
// thing standing between a user and that dead end.
func TestTicketInitWarnsWhenTheStoreRecordsNoActor(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer

	if code := runTicketIn(dir, []string{"--store", store, "init"}, &out, &errB); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if !strings.Contains(errB.String(), "records no actor") {
		t.Errorf("init did not warn about the empty actor roster: %q", errB.String())
	}
	if !strings.Contains(errB.String(), "config.yml") {
		t.Errorf("the warning does not name the file to fix: %q", errB.String())
	}

	// The warning has to describe a real refusal rather than a guess.
	out.Reset()
	errB.Reset()
	if code := runTicketIn(dir, []string{"--store", store, "create", "--title", "probe"}, &out, &errB); code == 0 {
		t.Error("create succeeded on a store with no actor, so the warning is wrong")
	}
}

// A store created with an actor is usable, and must not be warned about.
func TestTicketInitWithAnActorDoesNotWarn(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer

	if code := runTicketIn(dir, []string{"--store", store, "init", "--actor", "human:alex"}, &out, &errB); code != 0 {
		t.Fatalf("init --actor: exit %d, stderr %q", code, errB.String())
	}
	if strings.Contains(errB.String(), "records no actor") {
		t.Errorf("init --actor warned about an actor it was given: %q", errB.String())
	}

	out.Reset()
	errB.Reset()
	if code := runTicketIn(dir, []string{"--store", store, "create", "--title", "probe"}, &out, &errB); code != 0 {
		t.Errorf("create failed on a store that names an actor: exit %d, stderr %q", code, errB.String())
	}
}

// The warning belongs to the moment the store appears. Repeating it on
// every later command would train the user to ignore it, and `list` is
// not the place to argue about the roster.
func TestTicketWarnsOnceWhenTheStoreAppears(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer

	if code := runTicketIn(dir, []string{"--store", store, "init"}, &out, &errB); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if !strings.Contains(errB.String(), "records no actor") {
		t.Fatalf("precondition: init should have warned, got %q", errB.String())
	}

	out.Reset()
	errB.Reset()
	if code := runTicketIn(dir, []string{"--store", store, "list"}, &out, &errB); code != 0 {
		t.Fatalf("list: exit %d, stderr %q", code, errB.String())
	}
	if strings.Contains(errB.String(), "records no actor") {
		t.Errorf("a later command repeated the creation warning: %q", errB.String())
	}
}
