package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seedStore creates a real store holding one titled ticket, and returns the
// directory that HOLDS .tickets. The tools call the ticket package for real,
// so a fixture would only re-test the mocks.
func seedStore(t *testing.T, title string, labels []string) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := ticket.Init(dir, ticket.InitOptions{
		Actor:  ticket.Actor{ID: "agent:test", Name: "Test"},
		Labels: labels,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), ticket.CreateOptions{
		Title: title, Type: "task", Priority: "normal",
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// listTitles runs ticket_list through the tool, not through the store, so the
// test proves the redirect reaches an actual tool call.
func listTitles(t *testing.T, c *TicketCore) string {
	t.Helper()
	res, err := (&TicketListTool{TicketCore: c}).Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, blk := range res.Content {
		sb.WriteString(blockText(t, blk))
	}
	return sb.String()
}

func blockText(t *testing.T, blk any) string {
	t.Helper()
	b, err := json.Marshal(blk)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A configured path can name the directory that holds .tickets, the way
// ticket.Init and ticket.Discover treat a repository root. It can also name
// the .tickets directory itself. Both turn up in a hand-written config.
func TestOpenStoreAtAcceptsEitherPathForm(t *testing.T) {
	root := seedStore(t, "Either form", nil)

	for _, form := range []struct {
		why  string
		path string
	}{
		{"the directory that holds .tickets", root},
		{"the .tickets directory itself", filepath.Join(root, ticket.StoreDirName)},
	} {
		if _, err := openStoreAt(form.path); err != nil {
			t.Errorf("%s (%s): %v", form.why, form.path, err)
		}
	}
}

// ticket.Open requires a config file, so a typo must fail rather than open an
// empty store. An empty store reads exactly like a store with nothing to do.
func TestOpenStoreAtRefusesANonStore(t *testing.T) {
	plain := testsupport.TempDir(t)

	_, err := openStoreAt(plain)
	if err == nil {
		t.Fatal("opened a directory that is not a store; a typo would read as an empty store")
	}
	// The message must name both attempts, because the user cannot tell which
	// of the two readings we tried.
	for _, want := range []string{plain, ticket.StoreDirName, "git ticket init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q, so it does not say what to fix", err, want)
		}
	}
}

// The point of the feature: after a switch, a tool call reads the selected
// store and not the workspace one.
func TestSelectStoreRedirectsTheTools(t *testing.T) {
	workspace := seedStore(t, "Workspace ticket", nil)
	other := seedStore(t, "Personal ticket", nil)
	c := &TicketCore{CWD: workspace, Card: &TicketCard{CWD: workspace}}

	if got := listTitles(t, c); !strings.Contains(got, "Workspace ticket") {
		t.Fatalf("before any switch the tools must read the workspace store, got %s", got)
	}

	if err := c.SelectStore(TicketStoreSelection{Name: "personal", Path: other}); err != nil {
		t.Fatalf("select: %v", err)
	}

	got := listTitles(t, c)
	if !strings.Contains(got, "Personal ticket") {
		t.Errorf("after the switch the tools did not read the selected store, got %s", got)
	}
	if strings.Contains(got, "Workspace ticket") {
		t.Errorf("after the switch the tools still read the workspace store, got %s", got)
	}
	if name := c.ActiveStore().DisplayName(); name != "personal" {
		t.Errorf("active store is %q, want personal", name)
	}
}

// A selection that cannot be opened must change nothing. Leaving the tools
// pointed at a store that does not exist would strand the session.
func TestSelectStoreRefusalKeepsThePreviousStore(t *testing.T) {
	workspace := seedStore(t, "Workspace ticket", nil)
	c := &TicketCore{CWD: workspace, Card: &TicketCard{CWD: workspace}}

	err := c.SelectStore(TicketStoreSelection{Name: "ghost", Path: testsupport.TempDir(t)})
	if err == nil {
		t.Fatal("selected a path that holds no store")
	}
	if !c.ActiveStore().IsWorkspace() {
		t.Fatalf("a refused selection moved the active store to %+v", c.ActiveStore())
	}
	if got := listTitles(t, c); !strings.Contains(got, "Workspace ticket") {
		t.Errorf("a refused selection stranded the tools, got %s", got)
	}
}

// The label cache is keyed by store. Cached once per session, a switch would
// leave the previous store's vocabulary in the write schemas.
func TestLabelVocabularyFollowsTheStore(t *testing.T) {
	workspace := seedStore(t, "Workspace ticket", []string{"alpha"})
	other := seedStore(t, "Personal ticket", []string{"beta"})
	c := &TicketCore{CWD: workspace, Card: &TicketCard{CWD: workspace}}

	// Read first, so the workspace vocabulary is genuinely cached before the
	// switch. Without this read the test would pass on a lazy cache too.
	if got := c.labelVocabulary().Enum; len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("workspace vocabulary is %v, want [alpha]", got)
	}

	if err := c.SelectStore(TicketStoreSelection{Name: "personal", Path: other}); err != nil {
		t.Fatalf("select: %v", err)
	}

	if got := c.labelVocabulary().Enum; len(got) != 1 || got[0] != "beta" {
		t.Errorf("after the switch the vocabulary is %v, want [beta]; the cache did not follow the store", got)
	}

	// And back, to prove the cache serves each store rather than simply
	// tracking the most recent read.
	c.selectWorkspace()
	if got := c.labelVocabulary().Enum; len(got) != 1 || got[0] != "alpha" {
		t.Errorf("back on the workspace store the vocabulary is %v, want [alpha]", got)
	}
}

// A card rendered from the old store would describe tickets the next write
// will not touch.
func TestSelectStoreInvalidatesTheCard(t *testing.T) {
	workspace := seedStore(t, "Workspace ticket", nil)
	other := seedStore(t, "Personal ticket", nil)
	card := &TicketCard{CWD: workspace}
	c := &TicketCore{CWD: workspace, Card: card}
	c.BindCard()

	card.mu.Lock()
	card.fresh = true
	card.mu.Unlock()

	if err := c.SelectStore(TicketStoreSelection{Name: "personal", Path: other}); err != nil {
		t.Fatalf("select: %v", err)
	}

	card.mu.Lock()
	fresh := card.fresh
	card.mu.Unlock()
	if fresh {
		t.Error("the switch left the card fresh, so the next turn renders the previous store")
	}
}

// BindCard is what makes the card follow a switch. A card left unbound still
// discovers the workspace store, which is the drift this guards.
func TestBindCardPointsTheCardAtTheActiveStore(t *testing.T) {
	workspace := seedStore(t, "Workspace ticket", nil)
	other := seedStore(t, "Personal ticket", nil)
	card := &TicketCard{CWD: workspace}
	c := &TicketCore{CWD: workspace, Card: card}
	c.BindCard()

	if card.Open == nil {
		t.Fatal("BindCard left Open nil, so the card would discover the workspace store forever")
	}
	if err := c.SelectStore(TicketStoreSelection{Name: "personal", Path: other}); err != nil {
		t.Fatalf("select: %v", err)
	}

	s, err := card.Open()
	if err != nil {
		t.Fatalf("card open: %v", err)
	}
	all, err := s.List(context.Background(), ticket.Filter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Title != "Personal ticket" {
		t.Errorf("the card resolved a store holding %d ticket(s) %v, want the selected store", len(all), titles(all))
	}
}

func titles(ts []*ticket.Ticket) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Title)
	}
	return out
}

// toolText joins the text blocks of a tool result, which is where both the
// report and a refusal land.
func toolText(t *testing.T, content []provider.Content) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range content {
		if tb, ok := c.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// runStoreTool calls ticket_store and returns the parsed report. It fails the
// test when the tool refuses, so only the refusal tests read IsError.
func runStoreTool(t *testing.T, tool *TicketStoreTool, args string) ticketStoreReport {
	t.Helper()
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	res, err := tool.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := toolText(t, res.Content)
	if res.IsError {
		t.Fatalf("tool reported an error: %s", text)
	}
	var rep ticketStoreReport
	if err := json.Unmarshal([]byte(text), &rep); err != nil {
		t.Fatalf("decode report %q: %v", text, err)
	}
	return rep
}

func storeToolFixture(t *testing.T) (*TicketStoreTool, string, string) {
	t.Helper()
	workspace := seedStore(t, "Workspace ticket", nil)
	personal := seedStore(t, "Personal ticket", nil)
	c := &TicketCore{
		CWD:    workspace,
		Card:   &TicketCard{CWD: workspace},
		Stores: []config.TicketStore{{Name: "personal", Path: personal}},
	}
	c.BindCard()
	tool := &TicketStoreTool{TicketCore: c}
	return tool, workspace, personal
}

// With no argument the tool reads: it names the active store and the ones a
// caller may select, and it changes nothing.
func TestTicketStoreToolWithNoArgumentReports(t *testing.T) {
	tool, workspace, personal := storeToolFixture(t)

	rep := runStoreTool(t, tool, "")

	if rep.Active != config.WorkspaceTicketStoreName {
		t.Errorf("active is %q, want the workspace store before any switch", rep.Active)
	}
	if len(rep.Stores) != 2 {
		t.Fatalf("reported %d stores, want the workspace store and the configured one: %+v", len(rep.Stores), rep.Stores)
	}
	if rep.Stores[0].Name != config.WorkspaceTicketStoreName || rep.Stores[0].Path != workspace || !rep.Stores[0].Active {
		t.Errorf("first row is %+v, want the active workspace store at %s", rep.Stores[0], workspace)
	}
	if rep.Stores[1].Name != "personal" || rep.Stores[1].Path != personal || rep.Stores[1].Active {
		t.Errorf("second row is %+v, want the inactive personal store at %s", rep.Stores[1], personal)
	}
	if !tool.ActiveStore().IsWorkspace() {
		t.Error("a read moved the active store")
	}
}

func TestTicketStoreToolSwitchesAndReturns(t *testing.T) {
	tool, _, personal := storeToolFixture(t)

	rep := runStoreTool(t, tool, `{"store":"personal"}`)
	if rep.Active != "personal" || rep.ActivePath != personal {
		t.Fatalf("after the switch the report says %q at %q, want personal at %s", rep.Active, rep.ActivePath, personal)
	}
	if got := listTitles(t, tool.TicketCore); !strings.Contains(got, "Personal ticket") {
		t.Errorf("the sibling tools did not follow the switch, got %s", got)
	}

	// And back, because a session that cannot return is a trap.
	rep = runStoreTool(t, tool, `{"store":"workspace"}`)
	if rep.Active != config.WorkspaceTicketStoreName || rep.ActivePath != "" {
		t.Fatalf("after returning the report says %q at %q, want the workspace store", rep.Active, rep.ActivePath)
	}
	if got := listTitles(t, tool.TicketCore); !strings.Contains(got, "Workspace ticket") {
		t.Errorf("the sibling tools did not return to the workspace store, got %s", got)
	}
}

// An unknown name must name the valid ones. The model cannot guess them, and
// a bare refusal costs a turn.
func TestTicketStoreToolRefusesAnUnknownName(t *testing.T) {
	tool, _, _ := storeToolFixture(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"store":"nope"}`), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown store name was accepted")
	}
	text := toolText(t, res.Content)
	for _, want := range []string{"nope", "personal", config.WorkspaceTicketStoreName} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal %q does not mention %q", text, want)
		}
	}
	if !tool.ActiveStore().IsWorkspace() {
		t.Error("a refused name moved the active store")
	}
}

// The card must name a store the session selected, even when that store holds
// no claim. Otherwise a switch makes the card vanish and the model loses sight
// of where its next write lands.
//
// The first half is the guard that matters for everyone else: a session on the
// workspace store still renders nothing without a claim, so the common case
// pays no new context.
func TestTheCardNamesASelectedStore(t *testing.T) {
	workspace := seedStore(t, "Workspace ticket", nil)
	personal := seedStore(t, "Personal ticket", nil)
	now := time.Now()
	card := &TicketCard{
		CWD:     workspace,
		Session: func() string { return "sess-1" },
		Now:     func() time.Time { return now },
	}
	c := &TicketCore{CWD: workspace, Card: card}
	c.BindCard()

	if got := card.Ephemeral(); got != "" {
		t.Fatalf("the workspace store rendered a card with no claim, so every session now pays:\n%s", got)
	}

	if err := c.SelectStore(TicketStoreSelection{Name: "personal", Path: personal}); err != nil {
		t.Fatal(err)
	}

	got := card.Ephemeral()
	if !strings.Contains(got, "store: personal") {
		t.Errorf("after the switch the card does not name the active store:\n%s", got)
	}
}

// With a claim, the store leads and the claim follows, so a reader sees which
// ledger the claimed id belongs to before they read the id.
func TestTheCardNamesTheStoreAboveTheClaim(t *testing.T) {
	workspace := seedStore(t, "Workspace ticket", nil)
	personal := seedStore(t, "Personal ticket", nil)
	now := time.Now()
	card := &TicketCard{
		CWD:     workspace,
		Session: func() string { return "sess-1" },
		Now:     func() time.Time { return now },
	}
	c := &TicketCore{CWD: workspace, Card: card}
	c.BindCard()
	if err := c.SelectStore(TicketStoreSelection{Name: "personal", Path: personal}); err != nil {
		t.Fatal(err)
	}

	// Seeded through the core, so the ticket lands in the selected store.
	id, rev := readyTicket(t, c, []string{"do it"})
	claimWithSession(t, c, id, rev, "sess-1", 0)
	card.Invalidate()

	got := card.Ephemeral()
	if !strings.Contains(got, "store: personal") {
		t.Fatalf("the card does not name the store:\n%s", got)
	}
	if !strings.Contains(got, id) {
		t.Fatalf("the card does not name the claimed ticket:\n%s", got)
	}
	if strings.Index(got, "store:") > strings.Index(got, "claimed:") {
		t.Errorf("the store line must lead the claim line:\n%s", got)
	}
}

// A typo in a path disables one store. The tool reports it, because a silent
// drop leaves the user with a store that never appears and no reason why.
func TestTicketStoreToolReportsRefusedEntries(t *testing.T) {
	tool, _, _ := storeToolFixture(t)
	tool.Refused = []config.TicketStoreRefusal{{Name: "ops", Reason: `"ops-tickets" is relative`}}

	rep := runStoreTool(t, tool, "")

	if len(rep.Refused) != 1 || rep.Refused[0].Name != "ops" {
		t.Fatalf("refusals are %+v, want the ops entry reported", rep.Refused)
	}
	if !strings.Contains(rep.Refused[0].Reason, "relative") {
		t.Errorf("reason %q does not say what to fix", rep.Refused[0].Reason)
	}
}
