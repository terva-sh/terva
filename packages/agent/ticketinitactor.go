package agent

// Part 2 of TKT-01M1ZMDYPKS4MKCGDYWP5JVX2G: `terva ticket init` records an
// actor, the same way the ticket_init tool does.
//
// A store born with an empty roster refuses every write, and `init` then
// prints advice to go and create a ticket, which is the thing that fails. The
// tool has asked the user and passed --actor since it shipped. A person typing
// the command reached none of that, so the CLI is the path that produced the
// inert store.
//
// The resolution itself is not here. It is tools.ResolveNewStoreActor, shared
// with the tool, so a user who answers the question in one path is not asked
// again by the other. This file is only the CLI's half: finding an init in the
// argv, putting the flag in, and asking a person through stdin.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/tools"
)

// seedInitActor puts --actor into an `init` the user typed without one. It
// returns the argv to run, the actor recorded, and whether the user was asked
// this time.
//
// The caller must have established that no store exists at the target path.
// That is the second of two gates, and it is the one that makes this safe. An
// --actor injected into some other verb would sign a write with a name the
// user did not choose. With no store, no other verb can write at all, so a
// wrong verb here costs nothing.
func seedInitActor(dir string, argv []string, ask tools.TicketActorAsker) ([]string, string, bool) {
	verb := ticketVerbIndex(argv)
	if verb < 0 || argv[verb] != "init" || ticketArgvHasActor(argv) {
		return argv, "", false
	}
	actor, asked, err := tools.ResolveNewStoreActor(dir, ask)
	if err != nil || actor == "" {
		// A declined question and a failed one both leave the store as
		// `git ticket init` makes it, with warnNewStoreHasNoActor to say so.
		return argv, "", asked
	}
	// Inserted straight after the verb rather than appended. Go's flag package
	// stops at the first non-flag argument, so a trailing flag after a stray
	// positional would be parsed as another positional and silently dropped.
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[:verb+1]...)
	out = append(out, "--actor", actor)
	out = append(out, argv[verb+1:]...)
	return out, actor, asked
}

// reportSeededActor says which actor the store recorded and where the answer
// came from. An injected flag the user did not type has to be visible, or the
// next person to read the store's history cannot tell why it names who it
// names.
func reportSeededActor(actor string, asked bool, stderr io.Writer) {
	if actor == "" {
		return
	}
	if asked {
		fmt.Fprintf(stderr, "terva ticket: the store records %s as its actor.\n", actor)
		return
	}
	fmt.Fprintf(stderr,
		"terva ticket: the store records %s as its actor. You chose this actor for an earlier store. Pass --actor to record a different one.\n",
		actor)
}

// ticketVerbIndex is where the subcommand sits in the argv, or -1 for none.
//
// git-ticket registers its globals on the top-level flag set and again on each
// subcommand's, so `git ticket --store X init` and `git ticket init --store X`
// both work. The verb is the first argument that is not a flag and not the
// value of one.
//
// This is terva reading a grammar git-ticket owns, which is a cost. It is
// bounded by the caller's gates: the answer is used only to decide whether to
// seed an actor into a directory that has no store, and a wrong answer there
// leaves today's behaviour rather than producing a new one.
func ticketVerbIndex(argv []string) int {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			if i+1 < len(argv) {
				return i + 1
			}
			return -1
		}
		if !strings.HasPrefix(a, "-") {
			return i
		}
		if strings.Contains(a, "=") {
			continue
		}
		if ticketGlobalTakesValue(strings.TrimLeft(a, "-")) {
			// The next argument is this flag's value, not the verb.
			i++
		}
	}
	return -1
}

// ticketGlobalTakesValue names the globals that consume the next argument.
// Getting one wrong reads a flag's value as the verb, so a test drives each of
// them through a real `init` rather than asserting the list.
func ticketGlobalTakesValue(name string) bool {
	switch name {
	case "store", "if-revision", "actor", "lock-timeout":
		return true
	}
	return false
}

// ticketArgvHasActor reports whether the user named an actor themselves. An
// explicit answer wins over every stored or asked one, so this is the first
// thing seedInitActor checks.
func ticketArgvHasActor(argv []string) bool {
	for _, a := range argv {
		flag := strings.TrimLeft(a, "-")
		if flag == "actor" || strings.HasPrefix(flag, "actor=") {
			return true
		}
	}
	return false
}

// stdinTicketActorAsker asks the person at the terminal. It returns nil when
// stdin is not one, and a nil asker is how ResolveNewStoreActor declines to
// ask. That is what keeps `terva ticket init` in a script from blocking on a
// question nobody can answer.
func stdinTicketActorAsker() tools.TicketActorAsker {
	if !stdinIsTTY() {
		return nil
	}
	return func(suggestions []string) (string, bool, error) {
		return askTicketActor(os.Stdin, os.Stderr, suggestions)
	}
}

// askTicketActor is the question itself, over explicit streams so a test can
// answer it.
//
// The prompt goes to stderr, so a redirected stdout still shows it. The
// default is the first candidate from git config, which makes the common
// answer one keystroke.
func askTicketActor(in io.Reader, out io.Writer, suggestions []string) (string, bool, error) {
	var def string
	if len(suggestions) > 0 {
		def = suggestions[0]
	}

	fmt.Fprintln(out, "\nThis repository has no ticket store yet.")
	fmt.Fprintln(out, "A store records who makes each write. git-ticket signs a write that names no")
	fmt.Fprintln(out, "actor with the store's first actor, so that actor should be you.")
	fmt.Fprintln(out, "terva remembers your answer for the next store on this machine.")
	if len(suggestions) > 1 {
		fmt.Fprintf(out, "Other ids from your git config: %s\n", strings.Join(suggestions[1:], ", "))
	}
	if def != "" {
		fmt.Fprintf(out, "Which actor does the store record? [%s] ", def)
	} else {
		fmt.Fprint(out, "Which actor does the store record? Type an id, or none. ")
	}

	line, err := bufio.NewReader(in).ReadString('\n')
	// A closed stdin gives io.EOF with whatever was typed before it. The text
	// is the answer, and only an empty one is a decline.
	if err != nil && err != io.EOF {
		return "", false, nil
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		if def == "" {
			// No default and no answer. Nothing is remembered, so the next
			// store asks again rather than recording a guess.
			fmt.Fprintln(out, "No actor recorded. The next write will be refused.")
			return "", false, nil
		}
		answer = def
	}
	return answer, true, nil
}
