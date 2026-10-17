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

// TalkootManager is the team manager's place beside every talkoot on the
// daemon (docs/proposals/talkoot-team-manager.md). It holds no seat. It reads
// the teams and proposes changes to any of them, and a person decides each one.
type TalkootManager interface {
	// Teams lists every talkoot on the daemon.
	Teams() ([]ManagerTeam, error)
	// Roster describes one running talkoot, as a recruiter reads its own.
	Roster(talkoot string) (RecruitView, error)
	// Propose submits a proposal from the manager to one talkoot. persona,
	// when set, is the whole text of a new persona file the operations name.
	Propose(talkoot string, ops []talkoot.Op, persona, why string) (talkoot.Proposal, error)
	// Templates lists the templates a new team can start from.
	Templates() ([]talkoot.Template, error)
	// Draft checks a composed roster as the builder's preview does, and keeps
	// it as a draft for a person to review and create.
	Draft(id, text, why string) (ManagerDraft, error)
}

// ManagerDraft is a draft the manager saved, as the daemon previewed it.
type ManagerDraft struct {
	ID      string
	Budget  float64
	Members []talkoot.Member
	// Replaced is set when the draft took the place of an earlier draft of
	// the same id.
	Replaced bool
}

// ManagerTeam is one talkoot in the manager's list.
type ManagerTeam struct {
	ID      string
	Title   string
	State   string
	Running bool
	Problem string
	Members []talkoot.Member
}

const (
	talkootTeamsDesc = "List every talkoot on this daemon. The result gives each team's id, title, and state. For each team that this daemon runs, it also gives each member's id, role, and title. Only a team that this daemon runs can take a proposal. Read one team in full with talkoot_roster."

	talkootTeamsSchema = `{"type":"object","properties":{}}`

	talkootManagerRosterDesc = "Read one talkoot in full. The result lists each member with its job and its anti-jobs, but not its charter. It lists each installed driver, and the tool names that a tools list for that driver may give. It also lists the personas in the library. A persona in the library is not a member of the talkoot."

	talkootManagerRosterSchema = `{"type":"object","properties":{"talkoot":{"type":"string","description":"The id of the talkoot, as talkoot_teams gives it."}},"required":["talkoot"]}`

	talkootManagerProposeDesc = "Propose a change to the roster of one talkoot, for a person to decide. This tool never changes the roster. The proposal goes to the inbox of that talkoot as a card.\n\nAn op is add, edit, remove, look, or team. An add or an edit sets member fields, such as role, persona, driver, posture, tools, and budget_usd_per_day. Give the reason for the change in why.\n\nA team op names no member. It sets the team's title, color, charter, or budget_usd_per_day. The charter is the text that every member reads. No op waives the team budget.\n\nFor a new persona, put the whole persona file in persona. Give its name in the persona field of the member. An approval writes the persona file before it changes the roster."

	talkootTemplatesDesc = "List the templates a new team can start from, with each one's title, description, and member count. Give a name to read that template's whole text: its members and its charter. Start a draft from the closest template, and change what the person asks for."

	talkootTemplatesSchema = `{"type":"object","properties":{"name":{"type":"string","description":"The name of one template to read in full. Leave it out to list every template."}}}`

	talkootDraftDesc = "Save a new team as a draft, for a person to review and create. This tool never creates a team. The draft waits in the Talkoot app, where the person opens it in the team builder and presses Create.\n\nGive the whole talkoot.md in text: the frontmatter with name, title, budget_usd_per_day, and members, then the charter. Leave out home, because the person's directory sets it. A draft never waives the team budget.\n\nThe daemon checks the draft as the builder's preview does. It refuses a roster that breaks a rule or names a driver that is not installed. A draft with the id of an earlier draft replaces it."

	talkootDraftSchema = `{"type":"object","properties":{"id":{"type":"string","description":"The id of the new team. Use lower case letters, digits, and dashes, and begin with a letter."},"text":{"type":"string","description":"The whole talkoot.md of the new team, with its frontmatter and its charter, and without home."},"why":{"type":"string","description":"What the team is for. The person reads it on the draft."}},"required":["id","text","why"]}`

	talkootManagerProposeSchema = `{"type":"object","properties":{"talkoot":{"type":"string","description":"The id of the talkoot to change."},"ops":{"type":"array","maxItems":32,"items":{"type":"object","properties":{"op":{"type":"string","enum":["add","edit","remove","look","team"],"description":"The change to make to one member, or team for the team's own fields."},"member":{"type":"string","description":"The id of the member. For add, the id of the new member. Leave it out for team."},"set":{"type":"object","description":"The fields to set, by their names in the roster, as in {\"posture\":\"ask\"}. A null or empty value removes the field."}},"required":["op"]},"description":"The changes, in order. The person approves all of them, or none."},"persona":{"type":"string","description":"The whole text of a new persona file, with its frontmatter. Leave it out when the member uses a persona from the library."},"why":{"type":"string","description":"Why the team needs the change. The person reads it on the card."}},"required":["talkoot","ops","why"]}`
)

// ManagerTools returns the team manager's five tools. talkoot_roster and
// talkoot_propose take the member tools' names, so the permission policy
// classifies them the same way.
func ManagerTools(m TalkootManager) []core.Tool {
	return []core.Tool{&TalkootTeamsTool{Manager: m}, &TalkootManagerRosterTool{Manager: m}, &TalkootManagerProposeTool{Manager: m},
		&TalkootTemplatesTool{Manager: m}, &TalkootDraftTool{Manager: m}}
}

// TalkootTeamsTool lists the daemon's talkoots for the team manager.
type TalkootTeamsTool struct{ Manager TalkootManager }

func (t *TalkootTeamsTool) Name() string { return "talkoot_teams" }
func (t *TalkootTeamsTool) Description() string {
	return i18n.D("tool.talkoot_teams.description", talkootTeamsDesc)
}
func (t *TalkootTeamsTool) Schema() json.RawMessage { return json.RawMessage(talkootTeamsSchema) }
func (t *TalkootTeamsTool) Execute(_ context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Manager == nil {
		return core.ToolResult{}, errors.New("this session is not the team manager")
	}
	teams, err := t.Manager.Teams()
	if err != nil {
		return core.ToolResult{}, err
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: renderManagerTeams(teams)}}}, nil
}

// renderManagerTeams writes the list as plain lines. Every value came from a
// roster a person can edit, so each one is cut to one line.
func renderManagerTeams(teams []ManagerTeam) string {
	var b strings.Builder
	if len(teams) == 0 {
		return "No talkoot exists on this daemon yet.\n"
	}
	b.WriteString("Talkoots on this daemon:\n")
	for _, tm := range teams {
		fmt.Fprintf(&b, "- %s", oneLine(tm.ID))
		if tm.Title != "" {
			fmt.Fprintf(&b, ": %s", oneLine(tm.Title))
		}
		switch {
		case tm.Problem != "":
			fmt.Fprintf(&b, "; cannot load: %s", oneLine(tm.Problem))
		case !tm.Running:
			b.WriteString("; not running on this daemon, so its members are not listed and it cannot take a proposal")
		case tm.State != "":
			fmt.Fprintf(&b, "; %s", oneLine(tm.State))
		}
		b.WriteString("\n")
		for _, m := range tm.Members {
			fmt.Fprintf(&b, "  - %s (%s)", oneLine(m.ID), oneLine(m.Role))
			if m.Title != "" {
				fmt.Fprintf(&b, ": %s", oneLine(m.Title))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TalkootManagerRosterTool is talkoot_roster for the team manager. It names
// the talkoot to read.
type TalkootManagerRosterTool struct{ Manager TalkootManager }

func (t *TalkootManagerRosterTool) Name() string { return "talkoot_roster" }
func (t *TalkootManagerRosterTool) Description() string {
	return i18n.D("tool.talkoot_roster.manager", talkootManagerRosterDesc)
}
func (t *TalkootManagerRosterTool) Schema() json.RawMessage {
	return json.RawMessage(talkootManagerRosterSchema)
}
func (t *TalkootManagerRosterTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Manager == nil {
		return core.ToolResult{}, errors.New("this session is not the team manager")
	}
	var a struct {
		Talkoot string `json:"talkoot"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Talkoot) == "" {
		return core.ToolResult{}, errors.New("name the talkoot to read; talkoot_teams lists them")
	}
	v, err := t.Manager.Roster(a.Talkoot)
	if err != nil {
		return core.ToolResult{}, err
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: renderRecruitView(v)}}}, nil
}

// TalkootManagerProposeTool is talkoot_propose for the team manager. It names
// the talkoot, and it may propose a removal, as the person decided on
// 2026-10-14.
type TalkootManagerProposeTool struct{ Manager TalkootManager }

func (t *TalkootManagerProposeTool) Name() string { return "talkoot_propose" }
func (t *TalkootManagerProposeTool) Description() string {
	return i18n.D("tool.talkoot_propose.manager", talkootManagerProposeDesc)
}
func (t *TalkootManagerProposeTool) Schema() json.RawMessage {
	return json.RawMessage(talkootManagerProposeSchema)
}
func (t *TalkootManagerProposeTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Manager == nil {
		return core.ToolResult{}, errors.New("this session is not the team manager")
	}
	var a struct {
		Talkoot string       `json:"talkoot"`
		Ops     []talkoot.Op `json:"ops"`
		Persona string       `json:"persona"`
		Why     string       `json:"why"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Talkoot) == "" {
		return core.ToolResult{}, errors.New("name the talkoot to change; talkoot_teams lists them")
	}
	if strings.TrimSpace(a.Why) == "" {
		return core.ToolResult{}, errors.New("give the reason for the change in why, so the person can decide")
	}
	p, err := t.Manager.Propose(a.Talkoot, a.Ops, a.Persona, a.Why)
	if err != nil {
		return core.ToolResult{}, err
	}
	text := fmt.Sprintf("Proposal %s waits for a person in the inbox of %s: %s. The roster does not change until a person approves it.", p.ID, oneLine(a.Talkoot), oneLine(p.Summary))
	if p.Persona != nil {
		text += fmt.Sprintf(" An approval also writes the persona %s to the library.", oneLine(p.Persona.Name))
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: text}}}, nil
}

// TalkootTemplatesTool lists the templates for the team manager, or reads one.
type TalkootTemplatesTool struct{ Manager TalkootManager }

func (t *TalkootTemplatesTool) Name() string { return "talkoot_templates" }
func (t *TalkootTemplatesTool) Description() string {
	return i18n.D("tool.talkoot_templates.description", talkootTemplatesDesc)
}
func (t *TalkootTemplatesTool) Schema() json.RawMessage {
	return json.RawMessage(talkootTemplatesSchema)
}
func (t *TalkootTemplatesTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Manager == nil {
		return core.ToolResult{}, errors.New("this session is not the team manager")
	}
	var a struct {
		Name string `json:"name"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
		}
	}
	list, err := t.Manager.Templates()
	if err != nil {
		return core.ToolResult{}, err
	}
	text := func(s string) core.ToolResult {
		return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: s}}}
	}
	if name := strings.TrimSpace(a.Name); name != "" {
		for _, tm := range list {
			if tm.Name != name {
				continue
			}
			if tm.Problem != "" {
				return core.ToolResult{}, fmt.Errorf("template %s cannot be used: %s", oneLine(name), oneLine(tm.Problem))
			}
			return text(fmt.Sprintf("Template %s (%s):\n\n%s", oneLine(name), oneLine(tm.Source), tm.Raw)), nil
		}
		return core.ToolResult{}, fmt.Errorf("no template is named %s; call talkoot_templates with no name to list them", oneLine(name))
	}
	var b strings.Builder
	if len(list) == 0 {
		return text("No template is installed.\n"), nil
	}
	b.WriteString("Templates:\n")
	for _, tm := range list {
		fmt.Fprintf(&b, "- %s", oneLine(tm.Name))
		if tm.Title != "" {
			fmt.Fprintf(&b, ": %s", oneLine(tm.Title))
		}
		if tm.Problem != "" {
			fmt.Fprintf(&b, "; cannot be used: %s\n", oneLine(tm.Problem))
			continue
		}
		fmt.Fprintf(&b, "; %d members", tm.Members)
		if tm.Description != "" {
			fmt.Fprintf(&b, "; %s", oneLine(tm.Description))
		}
		b.WriteString("\n")
	}
	return text(b.String()), nil
}

// TalkootDraftTool saves a new team as a draft for the team manager.
type TalkootDraftTool struct{ Manager TalkootManager }

func (t *TalkootDraftTool) Name() string { return "talkoot_draft" }
func (t *TalkootDraftTool) Description() string {
	return i18n.D("tool.talkoot_draft.description", talkootDraftDesc)
}
func (t *TalkootDraftTool) Schema() json.RawMessage { return json.RawMessage(talkootDraftSchema) }
func (t *TalkootDraftTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Manager == nil {
		return core.ToolResult{}, errors.New("this session is not the team manager")
	}
	var a struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		Why  string `json:"why"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Text) == "" {
		return core.ToolResult{}, errors.New("give the whole talkoot.md of the new team in text")
	}
	if strings.TrimSpace(a.Why) == "" {
		return core.ToolResult{}, errors.New("say what the team is for in why, so the person can decide")
	}
	d, err := t.Manager.Draft(strings.TrimSpace(a.ID), a.Text, a.Why)
	if err != nil {
		return core.ToolResult{}, err
	}
	var b strings.Builder
	verb := "saved"
	if d.Replaced {
		verb = "replaced the earlier draft"
	}
	fmt.Fprintf(&b, "Draft %s %s. It waits in the Talkoot app for the person to review and create. No team exists until the person presses Create.\n", oneLine(d.ID), verb)
	fmt.Fprintf(&b, "Daily budget: $%g. Members:\n", d.Budget)
	for _, m := range d.Members {
		fmt.Fprintf(&b, "- %s (%s, %s, %s)\n", oneLine(m.ID), oneLine(m.Role), oneLine(m.Driver), oneLine(m.Posture))
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: b.String()}}}, nil
}
