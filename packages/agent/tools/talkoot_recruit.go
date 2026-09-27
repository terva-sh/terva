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

// TalkootRecruit is a recruiter session's place beside one talkoot, such as a
// Hautoja session. It holds no seat: it cannot send, hand off, or read notes.
// It reads the roster and proposes a member, and a person decides.
type TalkootRecruit interface {
	// Roster describes the talkoot for a recruiter.
	Roster() (RecruitView, error)
	// Propose submits a proposal from the recruiter. persona, when set, is
	// the whole text of a new persona file that the operations name.
	Propose(ops []talkoot.Op, persona, why string) (talkoot.Proposal, error)
}

// RecruitView is what a recruiter reads about its talkoot.
type RecruitView struct {
	Talkoot string
	Members []RecruitMember
	// Drivers are the drivers installed on this machine, native first.
	Drivers []RecruitDriver
	// Personas are the personas in the library. None of them is a member
	// until a member entry names it.
	Personas []RecruitPersona
}

// RecruitMember is one member with its persona's job and anti-jobs. The
// persona's charter stays out, so a recruiter reads what the member is for
// and not how it talks.
type RecruitMember struct {
	Member   talkoot.Member
	Summary  string
	AvoidFor []string
}

// RecruitDriver is one installed driver, and what a tools list for a member
// on it may name.
type RecruitDriver struct {
	Name  string
	Tools string
}

// RecruitPersona is one persona in the library.
type RecruitPersona struct {
	Name    string
	Summary string
}

const (
	talkootRecruitRosterDesc = "Read the talkoot that you recruit for. The result lists each member with its job and its anti-jobs, but not its charter. It lists each installed driver, and the tool names that a tools list for that driver may give. It also lists the personas in the library. A persona in the library is not a member of the talkoot."

	talkootRecruitProposeDesc = "Propose a new member, or a change to a member, for a person to decide. This tool never changes the roster. The proposal goes to the inbox of the talkoot as a card.\n\nAn op is add, edit, or look. An add or an edit sets member fields, such as role, persona, driver, posture, tools, and budget_usd_per_day. The tool refuses the posture yolo. Give the reason for the proposal in why.\n\nFor a new persona, put the whole persona file in persona. Give its name in the persona field of the member. An approval writes the persona file before it changes the roster."

	talkootRecruitProposeSchema = `{"type":"object","properties":{"ops":{"type":"array","maxItems":32,"items":{"type":"object","properties":{"op":{"type":"string","enum":["add","edit","look"],"description":"The change to make to one member."},"member":{"type":"string","description":"The id of the member. For add, the id of the new member."},"set":{"type":"object","description":"The fields to set, by their names in the roster, as in {\"posture\":\"ask\"}. A null or empty value removes the field."}},"required":["op","member"]},"description":"The changes, in order. The person approves all of them, or none."},"persona":{"type":"string","description":"The whole text of a new persona file, with its frontmatter. The frontmatter needs a name, a summary, and an avoid_for list. Leave it out when the member uses a persona from the library."},"why":{"type":"string","description":"Why the team needs the member. The person reads it on the card."}},"required":["ops","why"]}`
)

// RecruitTools returns the two tools of a recruiter session. They take the
// names of the member tools they stand in for, so the permission policy
// classifies them the same way.
func RecruitTools(r TalkootRecruit) []core.Tool {
	return []core.Tool{&TalkootRecruitRosterTool{Recruit: r}, &TalkootRecruitProposeTool{Recruit: r}}
}

// TalkootRecruitRosterTool is talkoot_roster for a recruiter.
type TalkootRecruitRosterTool struct{ Recruit TalkootRecruit }

func (t *TalkootRecruitRosterTool) Name() string { return "talkoot_roster" }
func (t *TalkootRecruitRosterTool) Description() string {
	return i18n.D("tool.talkoot_roster.recruiter", talkootRecruitRosterDesc)
}
func (t *TalkootRecruitRosterTool) Schema() json.RawMessage {
	return json.RawMessage(talkootRosterSchema)
}
func (t *TalkootRecruitRosterTool) Execute(_ context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Recruit == nil {
		return core.ToolResult{}, errors.New("this session does not recruit for a talkoot")
	}
	v, err := t.Recruit.Roster()
	if err != nil {
		return core.ToolResult{}, err
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: renderRecruitView(v)}}}, nil
}

// renderRecruitView writes the view as plain lines. Every value came from a
// roster or a persona file a person can edit, so each one is cut to one line.
func renderRecruitView(v RecruitView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Talkoot %s.\n\nMembers:\n", oneLine(v.Talkoot))
	if len(v.Members) == 0 {
		b.WriteString("- none yet\n")
	}
	for _, rm := range v.Members {
		m := rm.Member
		fmt.Fprintf(&b, "- %s", oneLine(m.ID))
		if m.Title != "" {
			fmt.Fprintf(&b, ": %s", oneLine(m.Title))
		}
		fmt.Fprintf(&b, "; role %s; driver %s", oneLine(m.Role), oneLine(m.Driver))
		if m.Posture != "" {
			fmt.Fprintf(&b, "; posture %s", oneLine(m.Posture))
		}
		if m.Persona != "" {
			fmt.Fprintf(&b, "; persona %s", oneLine(m.Persona))
		}
		if m.Tools != nil {
			fmt.Fprintf(&b, "; tools [%s]", oneLine(strings.Join(m.Tools, ", ")))
		}
		b.WriteString("\n")
		if rm.Summary != "" {
			fmt.Fprintf(&b, "  job: %s\n", oneLine(rm.Summary))
		}
		for _, a := range rm.AvoidFor {
			fmt.Fprintf(&b, "  avoid: %s\n", oneLine(a))
		}
	}
	b.WriteString("\nInstalled drivers:\n")
	for _, d := range v.Drivers {
		fmt.Fprintf(&b, "- %s: %s\n", oneLine(d.Name), oneLine(d.Tools))
	}
	b.WriteString("\nPersonas in the library (none of these is a member until a member entry names it):\n")
	for _, p := range v.Personas {
		fmt.Fprintf(&b, "- %s", oneLine(p.Name))
		if p.Summary != "" {
			fmt.Fprintf(&b, ": %s", oneLine(p.Summary))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TalkootRecruitProposeTool is talkoot_propose for a recruiter. It adds the
// persona draft, and it has no undo and no remove: a recruiter adds and
// reworks members, and a person removes one.
type TalkootRecruitProposeTool struct{ Recruit TalkootRecruit }

func (t *TalkootRecruitProposeTool) Name() string { return "talkoot_propose" }
func (t *TalkootRecruitProposeTool) Description() string {
	return i18n.D("tool.talkoot_propose.recruiter", talkootRecruitProposeDesc)
}
func (t *TalkootRecruitProposeTool) Schema() json.RawMessage {
	return json.RawMessage(talkootRecruitProposeSchema)
}
func (t *TalkootRecruitProposeTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Recruit == nil {
		return core.ToolResult{}, errors.New("this session does not recruit for a talkoot")
	}
	var a struct {
		Ops     []talkoot.Op `json:"ops"`
		Persona string       `json:"persona"`
		Why     string       `json:"why"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Why) == "" {
		return core.ToolResult{}, errors.New("give the reason for the member in why, so the person can decide")
	}
	p, err := t.Recruit.Propose(a.Ops, a.Persona, a.Why)
	if err != nil {
		return core.ToolResult{}, err
	}
	text := fmt.Sprintf("Proposal %s waits for a person in the inbox: %s. The roster does not change until a person approves it.", p.ID, oneLine(p.Summary))
	if p.Persona != nil {
		text += fmt.Sprintf(" An approval also writes the persona %s to the library.", oneLine(p.Persona.Name))
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: text}}}, nil
}
