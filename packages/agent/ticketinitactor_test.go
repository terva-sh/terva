package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// Part 2 of TKT-01M1ZMDYPKS4MKCGDYWP5JVX2G. `terva ticket init` used to create
// a store that refuses every write. These tests are about the actor it now
// records, and where that actor comes from.
//
// Every one of them pins TERVA_HOME. The remembered actor is a real user
// preference on the machine running the test, so without the pin these read
// the developer's own answer and pass or fail on it.

// gitSetup configures the repository the candidate ids are drawn from. It
// borrows package agent's own runGit rather than declaring a second one.
func gitSetup(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := runGit(context.Background(), dir, args...); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// ticketActorHome pins TERVA_HOME to an empty directory and optionally seeds
// the remembered actor in it.
func ticketActorHome(t *testing.T, remembered string) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if remembered == "" {
		return
	}
	if err := config.SetGlobalTicketActor(remembered); err != nil {
		t.Fatalf("seed the remembered actor: %v", err)
	}
}

// countingAsker answers the actor question and records that it was asked, so a
// test can prove a stored preference asks NOTHING.
type countingAsker struct {
	answer      string
	declined    bool
	asked       int
	suggestions []string
}

func (a *countingAsker) ask(suggestions []string) (string, bool, error) {
	a.asked++
	a.suggestions = suggestions
	if a.declined {
		return "", false, nil
	}
	return a.answer, true, nil
}

// writeATicket runs a real `create` against the store and returns the actor
// the store recorded on it.
//
// The write is the whole point. A test that reads config.yml proves what the
// file says, and the criterion is about what a write does. Reading CreatedBy
// back through the library also steps around the created_by trap: it is a
// nested map, so the created_by: line in the file is always empty and a text
// scan of that line reads "" for every ticket, pass or fail.
func writeATicket(t *testing.T, dir, store string) string {
	t.Helper()
	var out, errB bytes.Buffer
	code := runTicketInWithAsk(dir, []string{"--store", store, "--json", "create", "--title", "probe"}, &out, &errB, nil)
	if code != 0 {
		t.Fatalf("create on the new store: exit %d, stderr %q", code, errB.String())
	}
	var env struct {
		Ticket struct {
			ID string `json:"id"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("parse the create envelope %q: %v", out.String(), err)
	}
	if env.Ticket.ID == "" {
		t.Fatalf("the create envelope names no ticket: %q", out.String())
	}
	s, err := ticket.Open(store)
	if err != nil {
		t.Fatalf("open %s: %v", store, err)
	}
	tk, err := s.Get(context.Background(), env.Ticket.ID)
	if err != nil {
		t.Fatalf("read back %s: %v", env.Ticket.ID, err)
	}
	if tk.CreatedBy == nil {
		return ""
	}
	return tk.CreatedBy.ID
}

// Criterion 2. The second repository on a machine needs no flag, because the
// answer given for the first one is remembered.
func TestTicketInitSeedsTheRememberedActor(t *testing.T) {
	ticketActorHome(t, "human:alex")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer

	// A nil asker is a run with no way to reach a human. The remembered answer
	// has to be enough on its own.
	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, nil); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if strings.Contains(errB.String(), "records no actor") {
		t.Errorf("init warned about an empty roster it should have seeded: %q", errB.String())
	}
	if got := writeATicket(t, dir, store); got != "human:alex" {
		t.Errorf("the store recorded %q, want human:alex from the remembered preference", got)
	}
}

// The injected flag is one the user did not type, so the command says so. A
// store that quietly signs a name nobody chose is worse than one that refuses.
func TestTicketInitReportsTheActorItSeeded(t *testing.T) {
	ticketActorHome(t, "human:alex")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, nil); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	said := errB.String()
	if !strings.Contains(said, "human:alex") {
		t.Errorf("init did not name the actor it recorded: %q", said)
	}
	if !strings.Contains(said, "--actor") {
		t.Errorf("init did not say how to record a different actor: %q", said)
	}
}

// Criterion 3. Nothing is remembered and a human is reachable, so the command
// asks, and the answer serves every later store.
func TestTicketInitAsksAndRemembersTheAnswer(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	asker := &countingAsker{answer: "human:alex"}
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, asker.ask); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if asker.asked != 1 {
		t.Errorf("the actor question was put %d times, want 1", asker.asked)
	}
	if got := writeATicket(t, dir, store); got != "human:alex" {
		t.Errorf("the store recorded %q, want human:alex from the answer", got)
	}
	if got := config.GlobalUserPreferences().TicketActor; got != "human:alex" {
		t.Errorf("the answer was remembered as %q, want human:alex; the next store will ask again", got)
	}
}

// The promise the question makes is that it is asked once. A stored answer
// that still asks is the same interruption the preference exists to remove.
func TestTicketInitDoesNotAskWhenAnActorIsRemembered(t *testing.T) {
	ticketActorHome(t, "human:alex")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	asker := &countingAsker{answer: "human:wrong"}
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, asker.ask); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if asker.asked != 0 {
		t.Errorf("a remembered actor still put the question %d times", asker.asked)
	}
	if got := writeATicket(t, dir, store); got != "human:alex" {
		t.Errorf("the store recorded %q, want the remembered human:alex", got)
	}
}

// A chosen none is an answer and not a missing one, so it stops the asking and
// leaves the roster empty. The part 1 warning is what covers the user then.
func TestTicketInitHonoursARememberedNone(t *testing.T) {
	ticketActorHome(t, config.TicketActorNone)
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	asker := &countingAsker{answer: "human:alex"}
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, asker.ask); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if asker.asked != 0 {
		t.Errorf("a remembered none still put the question %d times", asker.asked)
	}
	if !strings.Contains(errB.String(), "records no actor") {
		t.Errorf("a store left with no actor was not warned about: %q", errB.String())
	}
}

// Criterion 4. A script has no terminal, so nothing can put a question to
// anybody. The store is still created and the command still returns.
func TestTicketInitInAScriptCreatesTheStoreAndNeverAsks(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer

	// The mechanism, not a stand-in for it. Under `go test` stdin is not a
	// terminal, which is the same condition a script runs under, and the
	// asker the real runTicketIn builds is nil there.
	if stdinTicketActorAsker() != nil {
		t.Fatal("stdin is not a terminal in a test, yet the CLI built an asker; a scripted init would block")
	}

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, stdinTicketActorAsker()); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if !ticketStoreExists(store) {
		t.Fatal("init without a terminal created no store")
	}
	if !strings.Contains(errB.String(), "records no actor") {
		t.Errorf("a store with no actor was not warned about: %q", errB.String())
	}
}

// An actor the user typed is the one answer nothing may override, and it is
// not remembered either: the preference belongs to the question, not to a flag.
func TestTicketInitKeepsTheActorTheUserTyped(t *testing.T) {
	ticketActorHome(t, "human:remembered")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init", "--actor", "human:typed"}, &out, &errB, nil); code != 0 {
		t.Fatalf("init --actor: exit %d, stderr %q", code, errB.String())
	}
	if got := writeATicket(t, dir, store); got != "human:typed" {
		t.Errorf("the store recorded %q, want the typed human:typed", got)
	}
}

// The safety property the whole design rests on. Seeding runs only where no
// store exists, and only for the init verb. An --actor put on any other verb
// would sign a write with a name the user never chose.
func TestSeedInitActorLeavesEveryOtherVerbAlone(t *testing.T) {
	ticketActorHome(t, "human:alex")
	dir := testsupport.TempDir(t)

	for _, argv := range [][]string{
		{"--store", "/tmp/x", "create", "--title", "init"},
		{"list"},
		{"ui"},
		{"--json", "check", "--strict"},
	} {
		got, actor, _ := seedInitActor(dir, argv, nil)
		if actor != "" {
			t.Errorf("seedInitActor(%v) recorded %q on a verb that is not init", argv, actor)
		}
		if len(got) != len(argv) {
			t.Errorf("seedInitActor(%v) rewrote the argv to %v", argv, got)
		}
	}
}

// The verb is not always argv[0]. git-ticket registers its globals on the
// top-level flag set too, so a value-taking global before the verb hides it.
// Reading one of those wrongly takes the flag's VALUE for the verb.
func TestTicketVerbIndex(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want int
	}{
		{[]string{"init"}, 0},
		{[]string{"--store", "/tmp/x", "init"}, 2},
		{[]string{"--store=/tmp/x", "init"}, 1},
		{[]string{"--json", "init"}, 1},
		{[]string{"--lock-timeout", "5s", "--json", "init"}, 3},
		{[]string{"--actor", "human:alex", "init"}, 2},
		{[]string{"create", "--title", "init"}, 0},
		{[]string{"--store", "init", "create"}, 2},
		{[]string{}, -1},
		{[]string{"--json"}, -1},
	} {
		if got := ticketVerbIndex(tc.argv); got != tc.want {
			t.Errorf("ticketVerbIndex(%v) = %d, want %d", tc.argv, got, tc.want)
		}
	}
}

// The table above asserts an index. This one drives the same argv shapes
// through a real init, so a wrong entry in ticketGlobalTakesValue fails on the
// store it produces rather than on an integer.
func TestTicketInitSeedsThroughALeadingGlobal(t *testing.T) {
	for _, lead := range [][]string{
		{"--json"},
		{"--lock-timeout", "30s"},
	} {
		t.Run(strings.Join(lead, " "), func(t *testing.T) {
			ticketActorHome(t, "human:alex")
			dir := testsupport.TempDir(t)
			store := filepath.Join(dir, ".tickets")
			argv := append(append([]string{}, lead...), "--store", store, "init")
			var out, errB bytes.Buffer

			if code := runTicketInWithAsk(dir, argv, &out, &errB, nil); code != 0 {
				t.Fatalf("init: exit %d, stderr %q", code, errB.String())
			}
			if got := writeATicket(t, dir, store); got != "human:alex" {
				t.Errorf("the store recorded %q, want human:alex; the leading global hid the verb", got)
			}
		})
	}
}

// The prompt's default is what makes the common answer one keystroke.
func TestAskTicketActorReadsAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name        string
		typed       string
		suggestions []string
		want        string
		wantOK      bool
	}{
		{"an id", "human:alex\n", []string{"human:drew"}, "human:alex", true},
		{"the default", "\n", []string{"human:drew", "human:d"}, "human:drew", true},
		{"none", "none\n", []string{"human:drew"}, "none", true},
		{"nothing to default to", "\n", nil, "", false},
		{"a closed stdin", "", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var prompt bytes.Buffer
			got, ok, err := askTicketActor(strings.NewReader(tc.typed), &prompt, tc.suggestions)
			if err != nil {
				t.Fatalf("askTicketActor: %v", err)
			}
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("askTicketActor(%q) = %q, %v; want %q, %v", tc.typed, got, ok, tc.want, tc.wantOK)
			}
			if !strings.Contains(prompt.String(), "actor") {
				t.Errorf("the prompt does not ask about an actor: %q", prompt.String())
			}
		})
	}
}

// The none answer travels as text and has to reduce to an empty actor id, or
// the store records a literal actor called none.
func TestAskedNoneRecordsNoActor(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	ask := func(suggestions []string) (string, bool, error) {
		return askTicketActor(strings.NewReader("none\n"), &bytes.Buffer{}, suggestions)
	}
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, ask); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if !strings.Contains(errB.String(), "records no actor") {
		t.Errorf("none left a roster that was not warned about: %q", errB.String())
	}
	if got := config.GlobalUserPreferences().TicketActor; got != config.TicketActorNone {
		t.Errorf("none was remembered as %q, want %q", got, config.TicketActorNone)
	}
}

// A declined question is not an answer. Nothing is stored, so the next store
// asks again rather than treating silence as none.
func TestADeclinedQuestionRemembersNothing(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	asker := &countingAsker{declined: true}
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, asker.ask); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if got := config.GlobalUserPreferences().TicketActor; got != "" {
		t.Errorf("a declined question stored %q", got)
	}
}

// The suggestions reach the question. Without them the prompt has no default
// and the common answer becomes typing.
func TestTheActorQuestionCarriesTheGitSuggestions(t *testing.T) {
	ticketActorHome(t, "")
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	gitSetup(t, dir, "init")
	gitSetup(t, dir, "config", "user.name", "Ada Lovelace")
	asker := &countingAsker{answer: "human:ada-lovelace"}
	var out, errB bytes.Buffer

	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, asker.ask); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	if len(asker.suggestions) == 0 {
		t.Fatal("the question was put with no candidates, so it has no default")
	}
	if asker.suggestions[0] != "human:ada-lovelace" {
		t.Errorf("the first candidate is %q, want human:ada-lovelace from user.name", asker.suggestions[0])
	}
}

// toolAsker is the ticket_init tool's question channel, answering with a fixed
// string. The CLI answers through stdin, so the two paths reach a user by
// different routes and must still land on the same store.
type toolAsker struct{ answer string }

func (a toolAsker) Ask(_ context.Context, _ []core.UserQuestion) ([]core.UserAnswer, error) {
	return []core.UserAnswer{{Answer: a.answer}}, nil
}

// Criterion 7. The same answer through either path produces the same store.
//
// This is the criterion that decides whether the two paths really share their
// rules or merely agree today. The comparison is the config.yml bytes, which
// git-ticket renders from the store's own config and which carry nothing
// derived from the path or the clock. Two inits with the same actor are
// identical files, so any difference here is a difference in the answer the
// two paths computed.
func TestTheCLIAndTheToolBuildTheSameStore(t *testing.T) {
	const actor = "human:alex"

	t.Run("from a remembered answer", func(t *testing.T) {
		ticketActorHome(t, actor)
		cli := initThroughTheCLI(t, nil)
		tool := initThroughTheTool(t, nil)
		sameConfig(t, cli, tool)
	})

	t.Run("from an answer given now", func(t *testing.T) {
		ticketActorHome(t, "")
		// The CLI reads its answer off stdin, the tool takes it from the front
		// end's question channel. Same answer, different route.
		cli := initThroughTheCLI(t, func(suggestions []string) (string, bool, error) {
			return askTicketActor(strings.NewReader(actor+"\n"), &bytes.Buffer{}, suggestions)
		})
		tool := initThroughTheTool(t, toolAsker{answer: actor})
		sameConfig(t, cli, tool)
	})
}

// initThroughTheCLI creates a store the way a person at a terminal does, and
// returns the store path.
func initThroughTheCLI(t *testing.T, ask tools.TicketActorAsker) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	store := filepath.Join(dir, ".tickets")
	var out, errB bytes.Buffer
	if code := runTicketInWithAsk(dir, []string{"--store", store, "init"}, &out, &errB, ask); code != 0 {
		t.Fatalf("cli init: exit %d, stderr %q", code, errB.String())
	}
	return store
}

// initThroughTheTool creates a store the way the model does, and returns the
// store path. The instructions block is off, because AGENTS.md is not what
// this comparison is about.
func initThroughTheTool(t *testing.T, asker core.Asker) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	tool := &tools.TicketInitTool{CWD: dir, AgentActorID: "agent:terva/mieli", Asker: asker}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"instructions":false}`), nil); err != nil {
		t.Fatalf("ticket_init: %v", err)
	}
	return filepath.Join(dir, ".tickets")
}

func sameConfig(t *testing.T, cliStore, toolStore string) {
	t.Helper()
	a, err := os.ReadFile(filepath.Join(cliStore, "config.yml"))
	if err != nil {
		t.Fatalf("read the cli store's config: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(toolStore, "config.yml"))
	if err != nil {
		t.Fatalf("read the tool store's config: %v", err)
	}
	// A guard on the comparison itself. Two empty rosters also match, and
	// would make this test pass while proving nothing.
	if strings.Contains(string(a), "actors: []") {
		t.Fatalf("the cli store records no actor, so the match below is vacuous:\n%s", a)
	}
	if string(a) != string(b) {
		t.Errorf("the two paths built different stores.\ncli:\n%s\ntool:\n%s", a, b)
	}
}

// A guard on the seam the two callers share. tools.ResolveNewStoreActor is
// what makes the CLI and the ticket_init tool agree, and a nil asker there is
// the headless path.
func TestResolveNewStoreActorWithoutAnAsker(t *testing.T) {
	ticketActorHome(t, "")
	actor, asked, err := tools.ResolveNewStoreActor(testsupport.TempDir(t), nil)
	if err != nil {
		t.Fatalf("ResolveNewStoreActor: %v", err)
	}
	if actor != "" || asked {
		t.Errorf("ResolveNewStoreActor with no asker = %q, asked %v; want no actor and no question", actor, asked)
	}
}
