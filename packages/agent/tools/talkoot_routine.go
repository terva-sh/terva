package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// TalkootRoutiner is a seat that can ask the person for a routine (decision
// 0029 rules 4 and 5). It is apart from TalkootSeat, as TalkootWaker is: only
// a native member on a daemon whose owner turned routines on has one.
type TalkootRoutiner interface {
	// ProposeRoutine files the ask as a proposal that waits for a person.
	ProposeRoutine(ask talkoot.RoutineAsk, why string) (TalkootRoutineReceipt, error)
}

// TalkootRoutineReceipt is what a seat returns for a routine it asked for.
type TalkootRoutineReceipt struct {
	Proposal talkoot.Proposal
	// Schedule is the schedule as the timer service reads it.
	Schedule string
	// Diffs names each limit that the ask sets to another value than the
	// default of its kind. The card shows these.
	Diffs []talkoot.RoutineDiff
}

// ⚠️ The language gate resolves a constant only inside its own file, so the
// text stays beside the Description method that returns it.
const (
	talkootRoutineDesc = "Ask the person for a routine. A routine is work on a schedule, with no person present. This tool only sends the request. The person reads it as a card, and approves it or declines it. The daemon stores nothing before the approval.\n\nGive a kind. A task does work from your prompt. A brief writes to the person from your prompt. A watcher runs your command with no model, and you get a turn only when the output changes.\n\nGive the schedule as text, such as \"every 15m\", \"daily 07:30\" or \"weekdays 07:30 Europe/Helsinki\". Give a prompt for a task or a brief, and a command for a watcher. Keep the prompt short, because each run reads it.\n\nEach kind has default limits that the person set. Leave a limit out to take its default. The card shows each limit that you change. Change a limit only when the work needs it, and give the cause in why.\n\nA run of a task or a brief works in a session apart from yours. It ends in a report to you, and you decide what to tell the team or the person. A run cannot send a message. It can ask the person a question, and the run ends when no answer comes. This version of terva runs no watcher yet. Tell the person that when you ask for a watcher."

	talkootRoutineSchema = `{"type":"object","properties":{"kind":{"type":"string","enum":["watcher","brief","task"],"description":"The kind of routine."},"schedule":{"type":"string","description":"When the routine runs, such as \"every 15m\", \"daily 07:30,19:00\", \"weekdays 07:30\" or \"mon,thu 09:00 Europe/Helsinki\"."},"prompt":{"type":"string","description":"What a task or a brief does at each run. At most 4096 bytes."},"command":{"type":"string","description":"The shell command that a watcher runs. The person reads it as you write it. At most 1024 bytes."},"why":{"type":"string","description":"The cause of the request, for the person who decides."},"end_after_runs":{"type":"integer","minimum":0,"description":"The routine ends after this number of runs. 0 is no such end."},"end_after_days":{"type":"integer","minimum":0,"description":"The routine ends this number of days after the approval. 0 is no such end."},"spend_usd_per_day":{"type":"number","minimum":0,"description":"The most that the runs may spend in one day, in dollars."},"reviews_per_day":{"type":"integer","minimum":0,"description":"The most review requests that the runs may make in one day."},"follow_ups":{"type":"integer","minimum":0,"description":"The number of wakes that one run may set for itself."},"catch_up":{"type":"boolean","description":"True gives one late run after a missed time. False gives none."}},"required":["kind","schedule","why"]}`
)

// TalkootRoutineTool asks the person for a routine on behalf of the member
// that calls it.
type TalkootRoutineTool struct{ Seat TalkootRoutiner }

func (t *TalkootRoutineTool) Name() string { return "talkoot_routine" }
func (t *TalkootRoutineTool) Description() string {
	return i18n.D("tool.talkoot_routine.description", talkootRoutineDesc)
}
func (t *TalkootRoutineTool) Schema() json.RawMessage { return json.RawMessage(talkootRoutineSchema) }
func (t *TalkootRoutineTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Seat == nil {
		return core.ToolResult{}, errors.New("this session is not a member of a talkoot")
	}
	var a struct {
		talkoot.RoutineAsk
		Why string `json:"why"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Why) == "" {
		return core.ToolResult{}, errors.New("give why: the cause of the request, for the person who decides")
	}
	r, err := t.Seat.ProposeRoutine(a.RoutineAsk, a.Why)
	if err != nil {
		return core.ToolResult{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Proposal %s asks the person for a routine: a %s, %s. It waits for the person's decision, and the daemon stores nothing before the approval. Do not ask again while it waits.", r.Proposal.ID, a.Kind, r.Schedule)
	if len(r.Diffs) == 0 {
		b.WriteString(" Every limit has the default of its kind.")
	} else {
		b.WriteString(" The card shows these limits, which differ from the defaults:")
		for _, d := range r.Diffs {
			fmt.Fprintf(&b, " %s %s (default %s).", d.Field, d.Value, d.Default)
		}
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: b.String()}}, Details: r.Proposal}, nil
}
