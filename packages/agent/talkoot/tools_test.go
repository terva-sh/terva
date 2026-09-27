package talkoot

import (
	"strings"
	"testing"
)

func TestMatchToolTakesANameOrAPrefix(t *testing.T) {
	list := []string{"read", "mcp_github_*"}
	for name, want := range map[string]bool{
		"read":             true,
		"reader":           false,
		"bash":             false,
		"mcp_github_issue": true,
		"mcp_gitlab_issue": false,
		// The seat stays whatever the list says.
		"talkoot_send":      true,
		"ask_user_question": true,
	} {
		if got := MatchTool(list, name, "core"); got != want {
			t.Errorf("MatchTool(%v, %q) = %v, want %v", list, name, got, want)
		}
	}
}

// 🚨 An mcp:<server> entry names one server by its tools' group. A name
// prefix cannot, because a server named github_x makes tools that start
// mcp_github_x_, and mcp_github_* matches them.
func TestAServerEntryStopsAtTheServer(t *testing.T) {
	for _, tc := range []struct {
		list        []string
		name, group string
		want        bool
	}{
		{[]string{"mcp:github"}, "mcp_github_issue", "mcp:github", true},
		{[]string{"mcp:github"}, "mcp_github_x_issue", "mcp:github_x", false},
		{[]string{"mcp_github_*"}, "mcp_github_x_issue", "mcp:github_x", true},
		// A group entry never matches by name, and a name never by group.
		{[]string{"mcp:github"}, "mcp:github", "core", false},
		{[]string{"github"}, "mcp_github_issue", "github", false},
	} {
		if got := MatchTool(tc.list, tc.name, tc.group); got != tc.want {
			t.Errorf("MatchTool(%v, %q, %q) = %v, want %v", tc.list, tc.name, tc.group, got, tc.want)
		}
	}
	// A seat name passes only as the built-in, whatever the list names.
	for _, list := range [][]string{{"read"}, {"talkoot_send"}, {"talkoot_*"}, {"talkoot_s*"}} {
		if MatchTool(list, "talkoot_send", "evil-extension") {
			t.Errorf("with list %v, an extension tool named talkoot_send passed", list)
		}
	}
	if !MatchTool([]string{"talkoot_*"}, "talkoot_extra", "evil-extension") {
		t.Error("a pattern should still match a tool that has no seat name")
	}
	if err := checkTools([]string{"mcp:github", "mcp:my.server-2"}); err != nil {
		t.Errorf("a server entry was refused: %v", err)
	}
	for _, bad := range []string{"mcp:", "mcp:git hub", "mcp:github*", "ext:github"} {
		if err := checkTools([]string{bad}); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

// 🚨 A card must mark every change that gives a member a tool it did not
// have. Absent is the full set.
func TestToolsWidenOnlyWhenTheMemberGainsATool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		was, is []string
		want    bool
	}{
		{"a list on a member that had the full set", nil, []string{"read"}, false},
		{"a list lifted", []string{"read"}, nil, true},
		{"a tool added", []string{"read"}, []string{"read", "bash"}, true},
		{"a tool dropped", []string{"read", "bash"}, []string{"read"}, false},
		{"a tool swapped", []string{"read"}, []string{"bash"}, true},
		{"no change", []string{"read"}, []string{"read"}, false},
		{"a name inside a pattern it had", []string{"mcp_github_*"}, []string{"mcp_github_issue"}, false},
		{"a pattern that covers more", []string{"mcp_github_issue"}, []string{"mcp_github_*"}, true},
		{"a narrower pattern", []string{"mcp_*"}, []string{"mcp_github_*"}, false},
		{"a seat tool listed", []string{"read"}, []string{"read", "talkoot_send"}, false},
		{"a server added", []string{"read"}, []string{"read", "mcp:github"}, true},
		{"the same server", []string{"mcp:github"}, []string{"mcp:github"}, false},
		{"a server inside every MCP tool", []string{"mcp_*"}, []string{"mcp:github"}, false},
		{"a server after a name prefix", []string{"mcp_github_*"}, []string{"mcp:github"}, true},
		{"neither has a list", nil, nil, false},
	} {
		c := MemberChange{Member: "jev", Before: &Member{ID: "jev", Role: RoleSpecialist, Tools: tc.was}, After: &Member{ID: "jev", Role: RoleSpecialist, Tools: tc.is}}
		applyDefaults(c.Before)
		applyDefaults(c.After)
		got := strings.Contains(strings.Join(Widens(c), ","), "tools")
		if got != tc.want {
			t.Errorf("%s: widens tools = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A proposal sets and drops a tools list in the text, keeps the comments, and
// its undo restores the member as it was.
func TestAProposalEditsTheToolsList(t *testing.T) {
	ops := []Op{{Op: OpEdit, Member: "atlas", Set: map[string]any{"tools": []any{"read", "grep"}}}}
	if err := CheckOps(mustParse(t, commented), ops); err != nil {
		t.Fatal(err)
	}
	out, err := ApplyOps([]byte(commented), ops)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "# Plans only, until the schema settles.") {
		t.Errorf("the comments are gone:\n%s", out)
	}
	before, after := mustParse(t, commented), mustParse(t, string(out))
	changes := Diff(before, after)
	if len(changes) != 1 || strings.Join(changes[0].After.Tools, ",") != "read,grep" {
		t.Fatalf("the change is %+v", changes)
	}
	if got := AuthorityChanges(changes[0]); strings.Join(got, ",") != "tools" {
		t.Errorf("the authority changes are %v", got)
	}
	undo, err := Inverse(changes)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ApplyOps(out, undo)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustParse(t, string(back)); got.Members[1].Tools != nil {
		t.Errorf("the undo left tools %v", got.Members[1].Tools)
	}
	// Lifting the list widens.
	lift := Diff(after, mustParse(t, string(back)))
	if len(lift) != 1 || !strings.Contains(strings.Join(Widens(lift[0]), ","), "tools") {
		t.Errorf("lifting the list should widen: %+v", lift)
	}
}

func TestAToolsValueIsAListOfStrings(t *testing.T) {
	r := mustParse(t, commented)
	for _, bad := range []any{"read", []any{"read", 3}, []any{strings.Repeat("x", MaxValueBytes+1)}} {
		if err := CheckOps(r, []Op{{Op: OpEdit, Member: "atlas", Set: map[string]any{"tools": bad}}}); err == nil {
			t.Errorf("tools %v passed", bad)
		}
	}
	if err := CheckOps(r, []Op{{Op: OpEdit, Member: "atlas", Set: map[string]any{"tools": []string{"read"}}}}); err != nil {
		t.Errorf("a []string: %v", err)
	}
}

// A list the person wrote in flow style stays in flow style.
func TestAToolsEditKeepsTheFlowStyle(t *testing.T) {
	text := strings.Replace(commented, "    posture: plan\n", "    posture: plan\n    tools: [read] # for now\n", 1)
	out, err := ApplyOps([]byte(text), []Op{{Op: OpEdit, Member: "atlas", Set: map[string]any{"tools": []any{"read", "grep"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "tools: [read, grep] # for now") {
		t.Errorf("the list lost its style or its comment:\n%s", out)
	}
}
