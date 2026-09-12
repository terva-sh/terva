package agent

// The `terva ticket` subcommand: slice 1 of docs/plans/git-ticket.md.
// The whole command surface is git-ticket's own cli package, embedded.
// One call and no second parser, so `terva ticket list` and
// `git ticket list` are the same code reached two ways, and a command
// added upstream appears here on the next module bump with no terva
// change.
//
// The `ui` command is the one exception to that automatic inheritance,
// and runTicketProcUI below explains why.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gtcli "github.com/terva-sh/git-ticket/cli"
	ticket "github.com/terva-sh/git-ticket/ticket"
	gtview "github.com/terva-sh/git-ticket/tui/view"

	"terva.sh/terva/packages/agent/tools"
)

// ExitCodeError carries the exit status of a delegated command whose own
// output already explained the outcome. main exits with the code and
// prints nothing more, so the delegated tool's stderr is the whole story.
type ExitCodeError struct{ Code int }

func (e ExitCodeError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// runTicketCommand handles `terva ticket ...`. The store is discovered
// from the directory the user ran terva in, exactly as `git ticket`
// discovers it; git-ticket's own --store flag covers unusual layouts.
func runTicketCommand(rawArgs []string) (bool, int) {
	if len(rawArgs) == 0 || rawArgs[0] != "ticket" {
		return false, 0
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "terva ticket:", err)
		return true, 1
	}
	return true, runTicketIn(dir, rawArgs[1:], os.Stdout, os.Stderr)
}

// runTicketIn is the testable core: git-ticket's embedded command surface
// against one directory and explicit streams. It returns git-ticket's
// documented exit status.
func runTicketIn(dir string, argv []string, stdout, stderr io.Writer) int {
	// `groom` is terva's verb and not git-ticket's, so it is answered here
	// rather than forwarded. The embedded CLI would refuse a word it does not
	// know.
	//
	// Only argv[0] is read. The comment above seedInitActor warns that reading
	// argv for a verb goes wrong quietly, because a title can be the word
	// `init` and a flag value can be anything. That warning is about SCANNING
	// argv for a verb in any position. argv[0] is the verb slot by grammar, so
	// no title or flag value ever lands there, and the warning does not reach
	// this line.
	if len(argv) > 0 && argv[0] == groomVerb {
		return runTicketGroom(dir, argv[1:], stdout, stderr)
	}
	return runTicketInWithAsk(dir, argv, stdout, stderr, stdinTicketActorAsker())
}

// runTicketInWithAsk is runTicketIn with the actor question supplied by the
// caller, so a test answers it without a terminal and no test blocks on one.
func runTicketInWithAsk(dir string, argv []string, stdout, stderr io.Writer, ask tools.TicketActorAsker) int {
	// Whether a store appears is how a created one is detected, rather
	// than reading argv for the `init` subcommand. git-ticket owns that
	// grammar, a title can be the word init, and a check that guesses at
	// somebody else's flags is a check that goes wrong quietly.
	path := ticketStorePath(dir, argv)
	existed := ticketStoreExists(path)

	// Seeding does read the argv for the verb, which the paragraph above
	// avoids. It has to: the flag goes in before the command runs, so the
	// store-appeared signal is not available yet. The `existed` gate is what
	// makes that safe. Where a store exists this never runs, so a verb read
	// wrongly cannot put an actor on somebody else's write.
	var actor string
	var asked bool
	if !existed {
		argv, actor, asked = seedInitActor(dir, argv, ask)
	}

	code := runTicketInWithUI(dir, argv, stdout, stderr, runTicketProcUI)

	if code == 0 && !existed {
		reportSeededActor(actor, asked, stderr)
		warnNewStoreHasNoActor(path, stderr)
	}
	return code
}

// ticketStorePath is where this command would put a store: the --store
// it names, or the conventional place under dir. Discovery is not used,
// because a store that does not exist yet cannot be discovered, and a
// parent repository's store would answer in its place.
func ticketStorePath(dir string, argv []string) string {
	for i, a := range argv {
		if a == "--store" && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--store="); ok {
			return v
		}
	}
	return filepath.Join(dir, ".tickets")
}

func ticketStoreExists(path string) bool {
	_, err := os.Stat(filepath.Join(path, "config.yml"))
	return err == nil
}

// warnNewStoreHasNoActor reports a store that was just created and can
// accept no writes.
//
// `git ticket init` writes an empty actor roster, and a store in that
// state refuses every mutation: "no actor given, and config.yml neither
// declares defaults.actor nor lists an actor". init then prints advice
// to go and create a ticket, which is the very thing that fails. The
// ticket_init TOOL avoids this by asking the user and passing --actor;
// a person typing the command reaches none of that, so they get the
// inert store and no hint about why.
//
// This only reports. It does not write a roster, because who signs a
// repository's history is the user's choice and not a default worth
// guessing at.
func warnNewStoreHasNoActor(path string, stderr io.Writer) {
	s, err := ticket.Open(path)
	if err != nil {
		return
	}
	if _, _, ok := s.Config().DefaultActor(); ok {
		return
	}
	fmt.Fprintf(stderr,
		"terva ticket: this store records no actor, so the next write is refused.\n%s\n",
		tools.ActorRepairHint(filepath.Join(path, "config.yml")))
}

// runTicketInWithUI is runTicketIn with the terminal binding supplied by
// the caller. A test passes its own, so `ui` can be driven to the point
// of the handoff without a tty, and so no test can seize the terminal it
// runs in.
func runTicketInWithUI(dir string, argv []string, stdout, stderr io.Writer, runUI func(gtcli.UIParams) error) int {
	return gtcli.Run(argv, gtcli.Env{
		Dir:    dir,
		Getenv: os.Getenv,
		Stdout: stdout,
		Stderr: stderr,
		RunUI:  runUI,
	})
}

// runTicketProcUI opens git-ticket's terminal UI over a resolved store.
//
// This is the one binding an embedding host does not get from cli.Run.
// git-ticket's cli package reaches a terminal only through Env.RunUI and
// never imports the terminal stack, so a host that wants only the
// command surface does not build it. Leaving the field nil is what made
// `terva ticket ui` answer "this entrypoint has no terminal UI wired".
//
// cli.UIParams and view.StoreParams mirror each other field for field,
// so the conversion is a cast. git-ticket documents that as deliberate:
// a field added to one struct and not the other fails this line at
// compile time, rather than dropping a value in silence.
//
// view.RunProcStore refuses when stdin or stdout is not a terminal, so a
// piped `terva ticket ui` gets that error instead of painting escape
// sequences into a pipe and blocking on keys nobody can type.
//
// The actor every write here records is resolved by cli before the
// handoff, and with no --actor it is the empty actor, which the store
// resolves to the first entry in config.yml. That is the human, not
// agent:terva/mieli, which is what a person driving the UI expects.
func runTicketProcUI(p gtcli.UIParams) error {
	return gtview.RunProcStore(gtview.StoreParams(p))
}
