package tools

// The ticket UI refuses a store that would reject every write, before it
// takes the screen.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gtcli "github.com/terva-sh/git-ticket/cli"
	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/testsupport"
)

// storeForTest initialises a store through git-ticket's own init, so the
// roster under test is the one a user actually gets. An empty actor
// means the default init, which writes `actors: []`.
func storeForTest(t *testing.T, actor string) *ticket.Store {
	t.Helper()
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, ".tickets")

	argv := []string{"--store", path, "init"}
	if actor != "" {
		argv = append(argv, "--actor", actor)
	}
	var out, errB bytes.Buffer
	if code := gtcli.Run(argv, gtcli.Env{
		Dir: dir, Getenv: os.Getenv, Stdout: &out, Stderr: &errB,
	}); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}

	s, err := ticket.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return s
}

// A default `git ticket init` leaves a store that refuses every
// mutation. Opening the UI over it would list nothing and fail on the
// first key that changes anything, from behind an alternate screen.
func TestTicketUIRefusesAStoreWithNoActor(t *testing.T) {
	s := storeForTest(t, "")

	err := ticketStoreCanWrite(gtcli.UIParams{Store: s})
	if err == nil {
		t.Fatal("the UI accepted a store whose every write is refused")
	}
	if !strings.Contains(err.Error(), "records no actor") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	if !strings.Contains(err.Error(), "config.yml") {
		t.Errorf("the refusal does not name the file to fix: %v", err)
	}
}

// The refusal has to describe a real failure and not a guess, so this
// proves the store really does reject a write.
func TestTicketUIRefusalMatchesARealRefusedWrite(t *testing.T) {
	s := storeForTest(t, "")
	if err := ticketStoreCanWrite(gtcli.UIParams{Store: s}); err == nil {
		t.Fatal("precondition: the store should have been refused")
	}

	// Path, not Root: Root is the enclosing Git repository and is empty
	// for a store in a bare temp directory, which would send this write
	// to a different store entirely and quietly pass.
	dir := filepath.Dir(s.Path())
	var out, errB bytes.Buffer
	code := gtcli.Run([]string{"--store", s.Path(), "create", "--title", "probe"}, gtcli.Env{
		Dir: dir, Getenv: os.Getenv, Stdout: &out, Stderr: &errB,
	})
	if code == 0 {
		t.Fatal("a write succeeded on the store the UI refused; the refusal is wrong")
	}
	if !strings.Contains(errB.String(), "actor") {
		t.Errorf("the write failed for some other reason than the actor: %q", errB.String())
	}
}

// A store with a roster is usable, and must open.
func TestTicketUIAcceptsAStoreWithAnActor(t *testing.T) {
	s := storeForTest(t, "human:alex")

	if err := ticketStoreCanWrite(gtcli.UIParams{Store: s}); err != nil {
		t.Fatalf("the UI refused a usable store: %v", err)
	}
}

// A caller that names an actor has already answered the question, so the
// empty roster is not its problem.
func TestTicketUIAcceptsANamedActorOverAnEmptyRoster(t *testing.T) {
	s := storeForTest(t, "")

	err := ticketStoreCanWrite(gtcli.UIParams{
		Store: s,
		Actor: ticket.Actor{ID: "human:alex"},
	})
	if err != nil {
		t.Fatalf("a named actor was refused because the roster is empty: %v", err)
	}
}
