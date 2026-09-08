package tools

// ticket_init: the tool that puts a ticket ledger in a repository that has
// none.
//
// The other eleven ticket_* tools register only where ticket.Discover finds a
// store, so a repository without one offers the model nothing and the model
// invents a scheme: a TODO file, a heading in a plan, a list in its own reply.
// The repair used to be leaving terva and running `terva ticket init` and
// `terva ticket instructions --write` by hand. This tool is those two commands,
// reachable from inside the session that needs them.
//
// It registers as the INVERSE of the store gate. Where a store exists this tool
// is absent and the eleven are present, so no session carries both halves and a
// repository with a ledger pays nothing for this one.
//
// The work goes through git-ticket's embedded cli package rather than its
// ticket package. Init is exported and the instructions text is not, so the
// command surface is the only place both halves exist, and reaching for it here
// keeps `terva ticket init --instructions` and this tool the same code.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	gtcli "github.com/terva-sh/git-ticket/cli"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// TicketInitTool creates the store and writes the agent workflow block.
//
// Asker is the front end's question channel, bound like AskUserTool's. It is
// what the actor question rides on, and a headless run leaves it nil.
type TicketInitTool struct {
	CWD string
	// AgentActorID is what the ticket_* tools will record once the store
	// exists, in the plan's shape (agent:terva/<persona>). It is offered as one
	// answer to the actor question and is never the default. Seeding it makes
	// the agent the store's fallback writer, and a later flag-less human write
	// is then signed with the agent's name.
	AgentActorID string
	Asker        core.Asker
}

type ticketInitArgs struct {
	// Instructions is a POINTER so an absent field ("the model did not say")
	// stays distinguishable from an explicit false. Absent means yes, because
	// the block is the half that tells the next session the store is there.
	Instructions *bool  `json:"instructions,omitempty"`
	Actor        string `json:"actor,omitempty"`
}

const ticketInitSchema = `{"type":"object","properties":{` +
	`"instructions":{"type":"boolean","description":"Write the agent workflow block to AGENTS.md. The default is true. Set this to false to create the store only."},` +
	`"actor":{"type":"string","description":"The actor id to record in the new store, for example human:alex. Omit this field. The tool then uses the choice that the user made before, or it asks the user."}` +
	`}}`

func (t *TicketInitTool) Name() string { return "ticket_init" }

func (t *TicketInitTool) Description() string {
	return i18n.D("tool.ticket_init.description", "Create a .tickets store in this repository, and write the agent workflow block to AGENTS.md. Use this tool when the user asks to track work as tickets and this repository has no store yet.\n\nThe tool does the same work as the command `terva ticket init --instructions`. It writes files in the repository, and it never publishes anything. Set instructions to false to create the store only.\n\nThe store records who makes each write. The tool asks the user which actor to record, and it remembers that answer for the next repository. Do not choose an actor yourself.\n\nAfter the store exists, the ten ticket_* tools become available. In most sessions they arrive on your next step, and the result of this tool tells you which.")
}

func (t *TicketInitTool) Schema() json.RawMessage { return json.RawMessage(ticketInitSchema) }

func (t *TicketInitTool) ToolGroupName() string { return "ticket" }

func (t *TicketInitTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a ticketInitArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return core.ToolResult{}, schemaArgsError(err)
		}
	}
	// The gate this tool exists to close. Registration already checked it, but
	// a session can outlive the check: another agent, another worktree, or the
	// user's own `git ticket init` in a second terminal.
	if TicketStoreAvailable(t.CWD) {
		return toolErr("ticket_init: a .tickets store already governs this directory, so there is nothing to create. The ticket_* tools work it directly."), nil
	}

	actor, asked, err := t.resolveActor(ctx, a.Actor)
	if err != nil {
		return core.ToolResult{}, err
	}

	writeInstructions := a.Instructions == nil || *a.Instructions
	argv := []string{"init"}
	if writeInstructions {
		argv = append(argv, "--instructions")
	}
	if actor != "" {
		argv = append(argv, "--actor", actor)
	}
	var out, errOut strings.Builder
	code := gtcli.Run(argv, gtcli.Env{
		Dir:    t.CWD,
		Getenv: func(string) string { return "" },
		Stdout: &out,
		Stderr: &errOut,
	})
	if code != 0 {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return toolErr(fmt.Sprintf("ticket_init: git-ticket refused the store (exit %d): %s", code, msg)), nil
	}

	// The refresh is what makes this usable in the session that ran it. Without
	// it the ten ticket_* tools wait for the next session, because registration
	// probed the store at build time and the store did not exist then.
	refreshed := core.AgentFromContext(ctx).RequestToolRefresh("ticket-init")

	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{
			Text: ticketInitReport(t.CWD, actor, asked, writeInstructions, refreshed),
		}},
		Details: map[string]any{
			"store":        filepath.Join(t.CWD, ".tickets"),
			"actor":        actor,
			"instructions": writeInstructions,
			"tools_live":   refreshed,
		},
	}, nil
}

// ticketInitReport tells the model what landed and, more usefully, when it can
// call the tools this unlocked. A result that says "the store is ready" and
// leaves that open invites a ticket_create on the next step in a host that
// cannot refresh, which fails as an unknown tool.
func ticketInitReport(cwd, actor string, asked, instructions, refreshed bool) string {
	var b strings.Builder
	b.WriteString("Created a ticket store at ")
	b.WriteString(filepath.Join(cwd, ".tickets"))
	b.WriteString(".")
	if instructions {
		b.WriteString(" The agent workflow block is in AGENTS.md.")
	}
	switch {
	case actor == "" && asked:
		b.WriteString(" The user chose no default actor, so every write names its own. Your ticket_* tools already do.")
	case actor == "":
		b.WriteString(" The store names no default actor, because no channel was available to ask the user. Every write names its own actor, and your ticket_* tools already do.")
	default:
		b.WriteString(" The store records " + actor + " as its first actor.")
	}
	if refreshed {
		b.WriteString(" The ten ticket_* tools are live on your next step. Commit the store when the user is ready.")
	} else {
		b.WriteString(" This session cannot re-resolve its tools, so the ten ticket_* tools arrive in the next session. Use the `terva ticket` command until then.")
	}
	return b.String()
}

// resolveActor answers who the new store records as its first actor. It returns
// the actor id (empty for none), and whether the answer came from the user this
// time.
//
// The order is deliberate. An explicit argument wins and is NOT remembered,
// because the model supplied it and the stored preference belongs to the user.
// Then the stored preference, which is why the second repository on a machine
// asks nothing. Then the user. Then nothing, which is what `git ticket init`
// does today and is the safe end of the range.
func (t *TicketInitTool) resolveActor(ctx context.Context, arg string) (string, bool, error) {
	if a := strings.TrimSpace(arg); a != "" {
		return a, false, nil
	}
	return ResolveNewStoreActor(t.CWD, t.askActor(ctx))
}

// askActor is this tool's way of putting the actor question to a user: the
// front end's question channel, with the candidates offered as options. A nil
// Asker returns a nil TicketActorAsker, which is how a headless run declines
// to ask rather than blocking.
func (t *TicketInitTool) askActor(ctx context.Context) TicketActorAsker {
	if t.Asker == nil {
		return nil
	}
	return func(suggestions []string) (string, bool, error) {
		const noneOption = "none: leave the store's actor list empty, and let every write name its own"
		options := append([]string{}, suggestions...)
		options = append(options, noneOption)
		if t.AgentActorID != "" {
			options = append(options, t.AgentActorID+": record me, the agent, as the store's first actor")
		}
		answers, err := t.Asker.Ask(ctx, []core.UserQuestion{{
			Question: "This repository has no ticket store yet. Which actor should a new store record as its first actor? git-ticket signs a write that names no actor with this one, so it should be you and not the agent. I will remember your answer for the next repository on this machine.",
			Slug:     "ticket actor",
			Options:  options,
			// The suggestions come from git config, and a person may want a
			// different handle entirely.
			AllowCustom: true,
		}})
		if err != nil {
			return "", false, fmt.Errorf("ticket_init: %w", err)
		}
		answers = core.PadAnswers(answers, 1)
		if answers[0].Declined {
			return "", false, nil
		}
		return answers[0].Answer, true, nil
	}
}

// The actor rules this tool used to carry now live in ticket_actor.go, so
// `terva ticket init` reaches the same answers. See ResolveNewStoreActor.
