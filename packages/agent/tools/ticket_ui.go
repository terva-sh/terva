package tools

// Running git-ticket's terminal UI on a terminal the caller supplies.
//
// `terva ticket ui` runs it on the process terminal, and that binding
// lives in packages/agent/ticketcmd.go. The interactive /ticket command
// cannot use that one: terva is already holding the terminal, so the UI
// has to render on a wrapper that leaves the host's raw mode alone and
// borrows the host's stdin reader.
//
// The difference between the two is one interface value, so everything
// else routes through here: store discovery, actor resolution, and the
// clipboard binding all stay git-ticket's own, and neither caller grows
// its own copy of them.
//
// This sits in tools rather than in packages/agent because the modes
// package cannot import packages/agent (that package imports modes).
// tools is the layer both sides already share, and it already embeds
// git-ticket's cli for ticket_init.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	gtcli "github.com/terva-sh/git-ticket/cli"
	gttui "github.com/terva-sh/git-ticket/tui"
	gtview "github.com/terva-sh/git-ticket/tui/view"
)

// RunTicketUIOn runs the ticket UI over the store that governs dir, and
// renders it on term. It returns when the user quits the UI.
//
// term carries the whole terminal contract, so a caller that is already
// running a full-screen application passes a wrapper rather than the
// process terminal. The UI writes to term and never to stdout, so no
// output reaches the caller's screen behind its back.
//
// The `ui` command resolves the store and the actor before it hands off,
// and it reports any failure there through the exit status rather than
// through the RunUI error. So the status is what decides the result, and
// the message comes from the captured stderr.
func RunTicketUIOn(dir string, term gttui.Terminal) error {
	if term == nil {
		return errors.New("the ticket UI needs a terminal to render on")
	}
	// stderr is captured rather than passed through. The caller owns a
	// painted screen, and a warning written straight to the tty would
	// land in the middle of it. On success the only thing written here
	// is git-ticket's note that no --actor was given, which the store
	// resolves for itself.
	var errBuf bytes.Buffer
	code := gtcli.Run([]string{"ui"}, gtcli.Env{
		Dir:    dir,
		Getenv: os.Getenv,
		Stdout: io.Discard,
		Stderr: &errBuf,
		RunUI: func(p gtcli.UIParams) error {
			if err := ticketStoreCanWrite(p); err != nil {
				return err
			}
			return gtview.Run(term,
				gtview.StoreLister(p.Store),
				gtview.StoreActions(gtview.StoreParams(p)),
			)
		},
	})
	if code == 0 {
		return nil
	}
	if msg := strings.TrimSpace(errBuf.String()); msg != "" {
		return errors.New(strings.TrimPrefix(msg, "git-ticket: "))
	}
	return fmt.Errorf("the ticket UI exited with status %d", code)
}

// ticketStoreCanWrite refuses a store that would reject every write.
//
// `git ticket init` writes an empty actor roster. A store in that state
// answers every mutation with "no actor given, and config.yml neither
// declares defaults.actor nor lists an actor", so the UI would open,
// list nothing, and fail on the first key that changes anything. Saying
// so before the alternate screen takes over costs the user one message
// instead of one confusing dead end.
//
// The rule is git-ticket's own: DefaultActor reports ok=false in exactly
// the case Store.Apply refuses. Asking it here rather than counting
// actors keeps the two in step if the fallback rule ever changes.
func ticketStoreCanWrite(p gtcli.UIParams) error {
	if p.Store == nil {
		return errors.New("no ticket store was resolved")
	}
	// A caller that named an actor has already answered the question,
	// and the roster does not have to.
	if p.Actor.ID != "" {
		return nil
	}
	if _, _, ok := p.Store.Config().DefaultActor(); ok {
		return nil
	}
	// Path is the store directory. Root is the enclosing Git repository,
	// and it is empty for a store outside one, which would name a
	// config.yml that is nowhere.
	// One line, not the block form. This error reaches /ticket's status
	// line as well as `terva ticket ui` stderr, and a status line renders
	// one row: newlines there scatter the text across the screen.
	return fmt.Errorf("this store records no actor, so every write would be refused - %s",
		ActorRepairLine(filepath.Join(p.Store.Path(), "config.yml")))
}
