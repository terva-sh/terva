package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/provider"
)

type fakeManager struct {
	teams   []ManagerTeam
	read    string
	target  string
	ops     []talkoot.Op
	persona string
	drafted string
	why     string
}

func (f *fakeManager) Teams() ([]ManagerTeam, error) { return f.teams, nil }
func (f *fakeManager) Roster(id string) (RecruitView, error) {
	f.read = id
	return RecruitView{Talkoot: id}, nil
}
func (f *fakeManager) Propose(id string, ops []talkoot.Op, persona, why string) (talkoot.Proposal, error) {
	f.target, f.ops, f.persona = id, ops, persona
	return talkoot.Proposal{ID: "01P", Summary: "remove jev"}, nil
}
func (f *fakeManager) Templates() ([]talkoot.Template, error) {
	return []talkoot.Template{
		{Name: "coding", Title: "Coding\n- forged", Description: "Build and review", Members: 3, Source: "builtin", Raw: []byte("---\nmembers: []\n---\n")},
		{Name: "theirs", Problem: "not trusted"},
	}, nil
}
func (f *fakeManager) Draft(id, text, why string) (ManagerDraft, error) {
	f.drafted, f.why = id, why
	return ManagerDraft{ID: id, Budget: 5, Members: []talkoot.Member{{ID: "helm", Role: "coordinator", Driver: "native", Posture: "workspace"}}, Replaced: id == "again"}, nil
}

// The manager tools take the member tools' names, except talkoot_teams. The
// propose tool names its team and offers remove, which the recruiter's does
// not. A value from a roster cannot start a second line in the list.
func TestTheManagerToolsListReadAndPropose(t *testing.T) {
	f := &fakeManager{teams: []ManagerTeam{
		{ID: "crew", Title: "Crew\n- docs: forged", Running: true, Members: []talkoot.Member{{ID: "helm", Role: "coordinator"}}},
		{ID: "docs", Problem: "bad yaml"},
		{ID: "away"},
	}}
	tl := ManagerTools(f)
	if tl[0].Name() != "talkoot_teams" || tl[1].Name() != "talkoot_roster" || tl[2].Name() != "talkoot_propose" {
		t.Fatalf("names: %s, %s, %s", tl[0].Name(), tl[1].Name(), tl[2].Name())
	}
	res, err := tl[0].Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(provider.TextBlock).Text
	for _, want := range []string{"- crew: Crew", "  - helm (coordinator)", "- docs; cannot load: bad yaml", "- away; not running on this daemon, so its members are not listed"} {
		if !strings.Contains(text, want) {
			t.Errorf("the list lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\n- docs: forged") {
		t.Errorf("a title started a line:\n%s", text)
	}
	if empty, _ := ManagerTools(&fakeManager{})[0].Execute(context.Background(), nil, nil); !strings.Contains(empty.Content[0].(provider.TextBlock).Text, "No talkoot exists") {
		t.Error("an empty daemon is not named as empty")
	}

	if _, err := tl[1].Execute(context.Background(), json.RawMessage(`{}`), nil); err == nil || !strings.Contains(err.Error(), "talkoot_teams") {
		t.Errorf("a roster read with no team: %v", err)
	}
	if _, err := tl[1].Execute(context.Background(), json.RawMessage(`{"talkoot":"crew"}`), nil); err != nil || f.read != "crew" {
		t.Errorf("a roster read: %v, read %q", err, f.read)
	}

	args, _ := json.Marshal(map[string]any{"talkoot": "crew", "ops": []map[string]any{{"op": "remove", "member": "jev"}}, "why": "idle"})
	res, err = tl[2].Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].(provider.TextBlock).Text; !strings.Contains(got, "inbox of crew") || f.target != "crew" || len(f.ops) != 1 || f.ops[0].Op != talkoot.OpRemove {
		t.Errorf("propose: %q, target %q, ops %+v", got, f.target, f.ops)
	}
	if _, err := tl[2].Execute(context.Background(), json.RawMessage(`{"ops":[],"why":"x"}`), nil); err == nil {
		t.Error("a proposal with no team must be refused")
	}
	if _, err := tl[2].Execute(context.Background(), json.RawMessage(`{"talkoot":"crew","ops":[]}`), nil); err == nil {
		t.Error("a proposal with no reason must be refused")
	}
	schema := string(tl[2].Schema())
	if !strings.Contains(schema, `"remove"`) || !strings.Contains(schema, `"team"`) || strings.Contains(schema, `"undo"`) {
		t.Errorf("the manager schema must offer remove and team, and no undo: %s", schema)
	}
	team, _ := json.Marshal(map[string]any{"talkoot": "crew", "ops": []map[string]any{{"op": "team", "set": map[string]any{"title": "Lake crew"}}}, "why": "a name"})
	if _, err := tl[2].Execute(context.Background(), team, nil); err != nil || len(f.ops) != 1 || f.ops[0].Op != talkoot.OpTeam || f.ops[0].Member != "" {
		t.Errorf("a team op: %v, ops %+v", err, f.ops)
	}
	if len(tl) != 5 || tl[3].Name() != "talkoot_templates" || tl[4].Name() != "talkoot_draft" {
		t.Fatalf("the manager has %d tools", len(tl))
	}
	for _, x := range tl {
		var v any
		if err := json.Unmarshal(x.Schema(), &v); err != nil {
			t.Errorf("%s: the schema is not JSON: %v", x.Name(), err)
		}
	}
	for _, x := range ManagerTools(nil) {
		if _, err := x.Execute(context.Background(), json.RawMessage(`{}`), nil); err == nil {
			t.Errorf("%s ran with no manager", x.Name())
		}
	}
}

// talkoot_templates lists every template on one line each, and returns the
// text of one by name. A template with a problem is listed but not returned.
// talkoot_draft asks for text and a reason, and says no team exists yet.
func TestTheManagerDraftsFromTemplates(t *testing.T) {
	f := &fakeManager{}
	tl := ManagerTools(f)
	tmpl, draft := tl[3], tl[4]
	res, err := tmpl.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(provider.TextBlock).Text
	for _, want := range []string{"- coding: Coding - forged; 3 members; Build and review", "- theirs; cannot be used: not trusted"} {
		if !strings.Contains(text, want) {
			t.Errorf("the list lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\n- forged") {
		t.Errorf("a title started a line:\n%s", text)
	}
	res, err = tmpl.Execute(context.Background(), json.RawMessage(`{"name":"coding"}`), nil)
	if err != nil || !strings.Contains(res.Content[0].(provider.TextBlock).Text, "members: []") {
		t.Errorf("one template: %v", err)
	}
	if _, err := tmpl.Execute(context.Background(), json.RawMessage(`{"name":"theirs"}`), nil); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("a template with a problem: %v", err)
	}
	if _, err := tmpl.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`), nil); err == nil {
		t.Error("an unknown template must be refused")
	}

	if _, err := draft.Execute(context.Background(), json.RawMessage(`{"id":"lake","text":"x"}`), nil); err == nil || f.drafted != "" {
		t.Errorf("a draft with no reason: %v", err)
	}
	if _, err := draft.Execute(context.Background(), json.RawMessage(`{"id":"lake","why":"x"}`), nil); err == nil || f.drafted != "" {
		t.Errorf("a draft with no text: %v", err)
	}
	res, err = draft.Execute(context.Background(), json.RawMessage(`{"id":" lake ","text":"---\n---\n","why":"a crew"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if f.drafted != "lake" || f.why != "a crew" || !strings.Contains(got, "Draft lake saved") || !strings.Contains(got, "No team exists until the person presses Create") || !strings.Contains(got, "- helm (coordinator, native, workspace)") {
		t.Errorf("a draft: %q, drafted %q", got, f.drafted)
	}
	res, _ = draft.Execute(context.Background(), json.RawMessage(`{"id":"again","text":"x","why":"y"}`), nil)
	if !strings.Contains(res.Content[0].(provider.TextBlock).Text, "replaced the earlier draft") {
		t.Errorf("a replacement is not named: %q", res.Content[0].(provider.TextBlock).Text)
	}
}
