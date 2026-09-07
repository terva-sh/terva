package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// allTicketTools is the full ten: the opt-out is all-or-nothing, so every
// assertion below walks both families rather than sampling one.
func allTicketTools() []string {
	return append(append([]string{}, ticketToolNames...), ticketWriteToolNames...)
}

func ticketsOff() *bool { off := false; return &off }
func ticketsOn() *bool  { on := true; return &on }

func assertNoTicketTools(t *testing.T, reg core.Registry, why string) {
	t.Helper()
	for _, name := range allTicketTools() {
		if _, ok := reg[name]; ok {
			t.Errorf("%s registered despite %s", name, why)
		}
	}
}

// --no-ticket (and --no-tickets) drop all ten tools even where a real store
// governs the cwd. The store gate says "these tools can answer here"; the flag
// says "do not offer them anyway", and the flag wins.
func TestTicketOptOutFlagDropsTools(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := ticketStoreDir(t)

	// The control: without the flag this same directory registers all ten.
	on := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	if _, ok := on["ticket_get"]; !ok {
		t.Fatal("no ticket tools without the flag either: the store gate is not what this test is measuring")
	}

	off := BuildToolRegistry(Args{NoTicket: true}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	assertNoTicketTools(t, off, "--no-ticket")
}

// The user layer's `tickets: false` does what the flag does, and lasts.
func TestTicketOptOutUserConfigDropsTools(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	writeUserConfig(t, home, config.Config{Tickets: ticketsOff()})

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)
	assertNoTicketTools(t, reg, "tickets:false on the user layer")
}

// A project may refuse the tools for its own directory. This layer is
// untrusted, and it is honored anyway: "do not register these tools here"
// only ever narrows the surface, and `terva ticket` still reaches the store.
func TestTicketOptOutProjectConfigDropsTools(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := ticketStoreDir(t)
	writeProjectConfig(t, dir, `{"tickets":false}`)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	assertNoTicketTools(t, reg, "tickets:false on the project layer")
}

// The direction that matters for safety: a cloned repository cannot hand
// itself back a tool set the user turned off. The project layer restricts and
// never escalates, the disable_mcp rule.
func TestTicketProjectConfigCannotReEnable(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	writeUserConfig(t, home, config.Config{Tickets: ticketsOff()})
	dir := ticketStoreDir(t)
	writeProjectConfig(t, dir, `{"tickets":true}`)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	assertNoTicketTools(t, reg, "a project's tickets:true over the user's false")
}

// A `tickets` key nobody wrote leaves the tools on: the default is on wherever
// a store governs the cwd, and only an explicit false turns it off. The
// project's own true is inert in both directions.
func TestTicketDefaultStaysOn(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	writeUserConfig(t, home, config.Config{Lore: ticketsOff()}) // an unrelated key
	dir := ticketStoreDir(t)
	writeProjectConfig(t, dir, `{"tickets":true}`)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	for _, name := range allTicketTools() {
		if _, ok := reg[name]; !ok {
			t.Errorf("%s missing though nothing turned the tools off", name)
		}
	}
}

// TicketsEnabled is the gate both the registry and the addendum read, so the
// layer rules are asserted on it directly too.
func TestTicketsEnabledLayerRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    *bool
		project string
		want    bool
	}{
		{"nothing set", nil, "", true},
		{"user false", ticketsOff(), "", false},
		{"user true", ticketsOn(), "", true},
		{"project false", nil, `{"tickets":false}`, false},
		{"project true", nil, `{"tickets":true}`, true},
		{"project cannot re-enable", ticketsOff(), `{"tickets":true}`, false},
		{"project can still disable", ticketsOn(), `{"tickets":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := testsupport.TempDir(t)
			t.Setenv("TERVA_HOME", home)
			writeUserConfig(t, home, config.Config{Tickets: tc.user})
			dir := testsupport.TempDir(t)
			if tc.project != "" {
				writeProjectConfig(t, dir, tc.project)
			}
			if got := config.TicketsEnabled(dir); got != tc.want {
				t.Errorf("TicketsEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// The addendum rides where the tools do, and it lands AFTER the AGENTS.md
// segment. That ordering is the mechanism, not a detail: a repository that
// documented `git ticket` on the command line before these tools existed
// keeps saying so in a file terva does not own, and recency is the only lever
// over it.
func TestTicketAddendumFollowsAgentsMD(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	dir := ticketStoreDir(t)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"),
		[]byte("# Project\n\nFile work with `git ticket create --title T`.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: dir}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	agents := segmentIndex(t, r, SourceAgentsMD)
	ticket := segmentIndex(t, r, SourceTicket)
	if agents < 0 {
		t.Fatal("no AGENTS.md segment: this test cannot measure the ordering it exists for")
	}
	if ticket < 0 {
		t.Fatal("no ticket segment though the tools registered")
	}
	if ticket < agents {
		t.Errorf("ticket segment at %d precedes AGENTS.md at %d; the correction must follow what it corrects", ticket, agents)
	}
	if !strings.Contains(r.SystemPrompt, "ticket_*") {
		t.Error("system prompt never names the ticket_* tools")
	}
}

// A session that opted out is not told to prefer tools it does not have. This
// is the terva_status rule (build_status_test.go) applied to a second surface.
func TestTicketAddendumAbsentWhenToolsAre(t *testing.T) {
	dir := ticketStoreDir(t)

	for _, tc := range []struct {
		name string
		args Args
	}{
		{"--no-ticket", Args{Provider: "openai", Model: "gpt-5", CWD: dir, NoTicket: true}},
		{"--no-tools", Args{Provider: "openai", Model: "gpt-5", CWD: dir, NoTools: true}},
		{"an allowlist without them", Args{Provider: "openai", Model: "gpt-5", CWD: dir, Tools: []string{"read"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERVA_HOME", testsupport.TempDir(t))
			t.Setenv("OPENAI_API_KEY", "test-key")
			r, err := Resolve(tc.args, false)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if i := segmentIndex(t, r, SourceTicket); i >= 0 {
				t.Errorf("ticket segment present under %s", tc.name)
			}
			if strings.Contains(r.SystemPrompt, "ticket_get") {
				t.Errorf("system prompt names ticket_get under %s, which cannot call it", tc.name)
			}
		})
	}
}

// Plan mode keeps the read five and prunes the write five, so the addendum
// keeps its base block and drops the revision protocol. The prompt must not
// teach if_revision to a session with no tool that takes it.
func TestTicketAddendumSplitsWithPlanMode(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	dir := ticketStoreDir(t)

	full, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: dir}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(full.SystemPrompt, "if_revision") {
		t.Error("a session with the write tools is never told the revision protocol")
	}

	plan, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: dir, Approval: string(core.ApprovalPlan)}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if segmentIndex(t, plan, SourceTicket) < 0 {
		t.Fatal("plan mode dropped the ticket segment though the read tools survive")
	}
	if strings.Contains(plan.SystemPrompt, "if_revision") {
		t.Error("plan mode teaches if_revision though it pruned every tool that takes one")
	}
}

// The opt-out removes the tools, never the ledger: the store on disk is
// untouched, and `terva ticket` reads it through git-ticket's own command
// surface, which never consults this key. That is what makes the flag an
// opt-out rather than a way to strand the store.
func TestTicketOptOutLeavesTheStoreOnDisk(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	writeUserConfig(t, home, config.Config{Tickets: ticketsOff()})
	dir := ticketStoreDir(t)

	reg := BuildToolRegistry(Args{NoTicket: true}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	assertNoTicketTools(t, reg, "the flag and the config key together")
	if _, err := os.Stat(filepath.Join(dir, ".tickets")); err != nil {
		t.Errorf("the store itself should be untouched by an opt-out: %v", err)
	}
}
