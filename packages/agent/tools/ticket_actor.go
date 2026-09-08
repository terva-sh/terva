package tools

import (
	"fmt"
	"os/exec"
	"strings"

	"terva.sh/terva/packages/agent/config"
)

// ActorRosterSnippet is the config.yml fragment that makes a store with an
// empty roster writable. It is the shape git-ticket itself renders: two
// spaces before the dash, and name aligned under id. A test renders a
// one-actor config through ticket.RenderConfig and fails if this drifts from
// what the store writes.
//
// The lines are flush left on purpose. A person pastes them into config.yml,
// where actors: sits at column zero. Indenting the block to make a message
// look tidy would hand the user text that breaks the file.
const ActorRosterSnippet = "actors:\n  - id: human:you\n    name: Your Name\n"

// ActorRepairHint says how to make a store with an empty roster writable.
// It carries newlines and an indented YAML body, so it belongs on a stream
// that prints lines as written. `terva ticket init` writes it to stderr.
//
// git-ticket has no command for this today. --actor is an init flag, and a
// second init on a store that exists returns store_exists, so the only exit
// is an editor. The upstream repair is a git ticket actor verb, and this
// text changes to name it once that lands.
func ActorRepairHint(configPath string) string {
	return fmt.Sprintf(
		"Replace `actors: []` in %s with these lines:\n\n%s\nOr delete the store, then run `terva ticket init --actor <id>`.",
		configPath, ActorRosterSnippet)
}

// ActorRepairLine is the same repair on one line, for a caller that renders
// a single row.
//
// The block form above cannot go in a status line. terva's status error is
// one string laid out as one row, so embedded newlines put each line on its
// own alignment and the YAML scatters across the screen. That shipped once.
//
// One id is a sufficient repair: git-ticket writes name as "" for an actor
// declared with --actor alone, and DefaultActor asks only that the roster
// hold an entry with an id. A test performs this exact edit and then writes
// a real ticket, so the shorter advice is checked rather than assumed.
func ActorRepairLine(configPath string) string {
	return fmt.Sprintf(
		"add `- id: human:you` under `actors:` in %s, or delete the store and run `terva ticket init --actor <id>`",
		configPath)
}

// ActorRosterMinimal is what ActorRepairLine tells a person to type. It is
// the smallest edit that makes a store writable, and the test that proves
// the line works splices exactly these bytes.
const ActorRosterMinimal = "actors:\n  - id: human:you\n"

// The rest of this file answers one question for two callers: who does a NEW
// store record as its first actor?
//
// The ticket_init TOOL asks the model's user through core.Asker. `terva ticket
// init` asks a person through stdin. They must reach the same answer from the
// same inputs, because a store the CLI made and a store the tool made are the
// same store, and a user who answers once should not be asked again by the
// other path. So the rules live here once, and each caller supplies only its
// own way of putting a question to a human.

// TicketActorAsker puts the actor question to a user and returns what they
// said. suggestions are the candidate ids drawn from git config, in the order
// to offer them.
//
// ok is false when the user declined, which is not an error and is not an
// answer: nothing is remembered and the next store asks again. A caller with
// no way to reach a human passes nil, which is how a headless run never
// blocks.
type TicketActorAsker func(suggestions []string) (answer string, ok bool, err error)

// ResolveNewStoreActor answers who a new store records, returning the actor id
// (empty for none) and whether the answer came from the user this time.
//
// The order is the tool's, unchanged: the stored preference, then the user,
// then nothing. An explicit actor is NOT handled here, because the two callers
// receive one differently. The tool takes a JSON field and the CLI reads a
// flag the user typed, and in both cases an explicit actor means this function
// is never called.
func ResolveNewStoreActor(cwd string, ask TicketActorAsker) (string, bool, error) {
	if actor, decided := StoredTicketActor(); decided {
		return actor, false, nil
	}
	if ask == nil {
		return "", false, nil
	}
	answer, ok, err := ask(TicketActorSuggestions(cwd))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	actor := ParseTicketActorAnswer(answer)
	RememberTicketActor(actor)
	return actor, true, nil
}

// StoredTicketActor reads the remembered answer. decided separates "the user
// chose none" from "the user has not been asked", which look alike in the
// actor id and must not: the first means stop asking.
func StoredTicketActor() (actor string, decided bool) {
	switch stored := strings.TrimSpace(config.GlobalUserPreferences().TicketActor); stored {
	case "":
		return "", false
	case config.TicketActorNone:
		return "", true
	default:
		return stored, true
	}
}

// RememberTicketActor stores the answer under the rule the question promises,
// so the next repository on this machine asks nothing. A chosen none is stored
// as a real answer rather than as an absent one.
//
// A failed write is dropped. The store is already created and correct at this
// point, and refusing the command because a preference did not persist would
// trade a working store for a tidy one.
func RememberTicketActor(actor string) {
	if actor == "" {
		actor = config.TicketActorNone
	}
	_ = config.SetGlobalTicketActor(actor)
}

// ParseTicketActorAnswer reads one answer back into an actor id. An offered
// option carries its explanation after a colon-space, and a written-in answer
// carries whatever the user typed. Both reduce to the first field, because an
// actor id has no spaces in it.
//
// The none answer is recognised by that first field, so a caller never has to
// compare against the option text it built.
func ParseTicketActorAnswer(answer string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(answer), " ")
	first = strings.TrimSuffix(first, ":")
	if first == "" || strings.EqualFold(first, config.TicketActorNone) {
		return ""
	}
	return first
}

// TicketActorSuggestions builds candidate actor ids from this repository's git
// identity, so the common answer is one keystroke and not typing.
//
// user.name gives the friendlier handle and the email's local part gives the
// stable one. Both are offered when they differ, because neither is reliably
// the one a person wants: a name is often two words, and a work email often
// starts with an initial.
func TicketActorSuggestions(cwd string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		slug := actorSlug(raw)
		if slug == "" || seen[slug] {
			return
		}
		seen[slug] = true
		out = append(out, "human:"+slug)
	}
	add(gitConfigValue(cwd, "user.name"))
	if email := gitConfigValue(cwd, "user.email"); email != "" {
		local, _, _ := strings.Cut(email, "@")
		add(local)
	}
	return out
}

func gitConfigValue(cwd, key string) string {
	cmd := exec.Command("git", "-C", cwd, "config", "--get", key)
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// actorSlug reduces a display name to the shape an actor id takes: lower case,
// no spaces. "Drew Short" becomes "drew-short".
func actorSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
