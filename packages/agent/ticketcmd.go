package agent

// The `terva ticket` subcommand: slice 1 of docs/plans/git-ticket.md.
// The whole command surface is git-ticket's own cli package, embedded.
// One call and no second parser, so `terva ticket list` and
// `git ticket list` are the same code reached two ways, and a command
// added upstream appears here on the next module bump with no terva
// change.

import (
	"fmt"
	"io"
	"os"

	gtcli "github.com/terva-sh/git-ticket/cli"
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
	return gtcli.Run(argv, gtcli.Env{
		Dir:    dir,
		Getenv: os.Getenv,
		Stdout: stdout,
		Stderr: stderr,
	})
}
