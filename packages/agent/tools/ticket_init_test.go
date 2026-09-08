package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// initAsker answers the actor question with a fixed string and counts the
// calls, so a test can assert that a stored preference asks NOTHING.
type initAsker struct {
	answer   string
	declined bool
	asked    int
	question core.UserQuestion
}

func (a *initAsker) Ask(_ context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	a.asked++
	if len(qs) > 0 {
		a.question = qs[0]
	}
	return []core.UserAnswer{{Answer: a.answer, Declined: a.declined}}, nil
}

// runInit executes the tool against dir and returns the result. The TERVA_HOME
// the caller set is where the remembered actor lands.
func runInit(t *testing.T, tool *TicketInitTool, args string) core.ToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("ticket_init: %v", err)
	}
	return res
}

// initDetails reads the tool's structured half, which a host surfaces and a
// test asserts on. Details is an any, so the assertion is the reader.
func initDetails(t *testing.T, res core.ToolResult) map[string]any {
	t.Helper()
	d, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("ticket_init returned details of type %T, want map[string]any", res.Details)
	}
	return d
}

// The whole point of the tool, end to end: a directory with no ledger comes
// out of one call with a store the ticket_* tools can discover, and with the
// agent workflow block in AGENTS.md.
func TestTicketInitCreatesTheStoreAndTheInstructions(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	tool := &TicketInitTool{CWD: dir, AgentActorID: "agent:terva/mieli"}
	res := runInit(t, tool, `{}`)
	if res.IsError {
		t.Fatalf("ticket_init reported an error: %v", res.Details)
	}
	if !TicketStoreAvailable(dir) {
		t.Fatal("the store is not discoverable after ticket_init, so the ten ticket_* tools would still not register")
	}
	block, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("no AGENTS.md after the default call: %v", err)
	}
	if !strings.Contains(string(block), "git-ticket") {
		t.Error("AGENTS.md exists but carries no git-ticket block")
	}
	if got := initDetails(t, res)["instructions"]; got != true {
		t.Errorf("details say instructions=%v though the block was written", got)
	}
}

// instructions:false is the one half a caller may skip, and skipping it must
// leave the file alone rather than write an empty one.
func TestTicketInitSkipsTheInstructionsWhenAsked(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	res := runInit(t, &TicketInitTool{CWD: dir}, `{"instructions":false}`)
	if res.IsError {
		t.Fatalf("ticket_init reported an error: %v", res.Details)
	}
	if !TicketStoreAvailable(dir) {
		t.Fatal("no store though only the instructions were declined")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md exists though instructions were false (%v)", err)
	}
}

// Registration keeps this tool away from a repository that has a store, but a
// session outlives that check: another agent, another worktree, or the user's
// own `git ticket init` in a second terminal. The refusal names what happened
// instead of creating a second store or failing obscurely.
func TestTicketInitRefusesAnExistingStore(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)
	runInit(t, &TicketInitTool{CWD: dir}, `{}`)

	res := runInit(t, &TicketInitTool{CWD: dir}, `{}`)
	if !res.IsError {
		t.Fatal("a second ticket_init on the same directory was accepted")
	}
	if !strings.Contains(resultText(res), "already") {
		t.Errorf("the refusal does not say a store is already there: %q", resultText(res))
	}
}

// The user is asked once per machine, not once per repository. That promise is
// the reason the answer is stored in the user config layer at all, so the
// second repository must ask nothing and still seed the same actor.
func TestTicketInitAsksOnceAndRemembersTheActor(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	first := testsupport.TempDir(t)
	asker := &initAsker{answer: "human:alex"}
	runInit(t, &TicketInitTool{CWD: first, Asker: asker}, `{}`)
	if asker.asked != 1 {
		t.Fatalf("the first repository asked %d times, want 1", asker.asked)
	}
	if got := config.GlobalUserPreferences().TicketActor; got != "human:alex" {
		t.Errorf("the answer was not remembered: ticket_actor = %q", got)
	}
	assertStoreActor(t, first, "human:alex")

	second := testsupport.TempDir(t)
	again := &initAsker{answer: "human:someone-else"}
	runInit(t, &TicketInitTool{CWD: second, Asker: again}, `{}`)
	if again.asked != 0 {
		t.Errorf("the second repository asked again (%d times) though the choice was already stored", again.asked)
	}
	assertStoreActor(t, second, "human:alex")
}

// "No default actor" is an ANSWER, not an absence of one. Storing it as such
// is what stops the question coming back in every repository, and the store
// itself must then name nobody.
func TestTicketInitRemembersTheNoneAnswer(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	asker := &initAsker{answer: "none: leave the store's actor list empty, and let every write name its own"}
	runInit(t, &TicketInitTool{CWD: dir, Asker: asker}, `{}`)

	if got := config.GlobalUserPreferences().TicketActor; got != config.TicketActorNone {
		t.Errorf("ticket_actor = %q, want %q so the question is not asked again", got, config.TicketActorNone)
	}
	assertStoreActor(t, dir, "")

	next := &initAsker{answer: "human:alex"}
	runInit(t, &TicketInitTool{CWD: testsupport.TempDir(t), Asker: next}, `{}`)
	if next.asked != 0 {
		t.Error("the stored none answer did not stop the question")
	}
}

// A declined question stores nothing. The user said "not now", which is not
// the same as "never name an actor", and the next repository asks again.
func TestTicketInitDeclinedQuestionStoresNothing(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	asker := &initAsker{declined: true}
	runInit(t, &TicketInitTool{CWD: dir, Asker: asker}, `{}`)
	if got := config.GlobalUserPreferences().TicketActor; got != "" {
		t.Errorf("a declined question stored %q", got)
	}
	assertStoreActor(t, dir, "")
}

// A headless run has nobody to ask, and it must not guess. Seeding the agent
// there would make every later flag-less human write in that store arrive
// signed with the agent's name.
func TestTicketInitWithoutAnAskChannelSeedsNobody(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	res := runInit(t, &TicketInitTool{CWD: dir, AgentActorID: "agent:terva/mieli"}, `{}`)
	if res.IsError {
		t.Fatalf("ticket_init reported an error: %v", res.Details)
	}
	assertStoreActor(t, dir, "")
	if got := config.GlobalUserPreferences().TicketActor; got != "" {
		t.Errorf("a run with no ask channel stored %q as the user's choice", got)
	}
}

// The refresh is what makes the tool usable in the session that called it.
// Without it the ten ticket_* tools wait for the next session, because
// registration probed the store before it existed.
func TestTicketInitAsksTheHostToReResolveItsTools(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	ag := core.NewAgent(nil, "test-model", "", core.Registry{})
	var reasons []string
	ag.SetToolRefresher(func(reason string) { reasons = append(reasons, reason) })

	tool := &TicketInitTool{CWD: dir}
	res, err := tool.Execute(core.ContextWithAgent(context.Background(), ag), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("ticket_init: %v", err)
	}
	if len(reasons) != 1 {
		t.Fatalf("the host was asked to rebuild %d times, want 1 (%v)", len(reasons), reasons)
	}
	if initDetails(t, res)["tools_live"] != true {
		t.Error("the result does not tell the model its ticket tools are live")
	}
	if !strings.Contains(resultText(res), "next step") {
		t.Errorf("the result does not say when the tools arrive: %q", resultText(res))
	}
}

// A host with no rebuild (a one-shot print or cli run) is told so, in the
// result. A model that reads "the store is ready" and calls ticket_create on
// its next step in such a session gets an unknown-tool error instead.
func TestTicketInitSaysWhenTheToolsWaitForTheNextSession(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	res := runInit(t, &TicketInitTool{CWD: dir}, `{}`)
	if initDetails(t, res)["tools_live"] != false {
		t.Error("a session with no refresher claims its tools are live")
	}
	if !strings.Contains(resultText(res), "next session") {
		t.Errorf("the result does not name the session boundary: %q", resultText(res))
	}
}

// The options carry an explanation after the id, and a written-in answer
// carries whatever the user typed. Both reduce to the actor id, and the none
// option reduces to no actor at all.
func TestParseActorAnswer(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"human:alex", "human:alex"},
		{"human:alex: the name from your git config", "human:alex"},
		{"none: leave the store's actor list empty", ""},
		{"none", ""},
		{"  agent:terva/mieli  ", "agent:terva/mieli"},
		{"", ""},
	} {
		if got := ParseTicketActorAnswer(tc.in); got != tc.want {
			t.Errorf("ParseTicketActorAnswer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A git display name is prose and an actor id is not, so the suggestion is
// slugged before it is offered.
func TestActorSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Drew Short", "drew-short"},
		{"  ALEX  ", "alex"},
		{"a.b_c", "a.b_c"},
		{"Ada Lovelace (work)", "ada-lovelace-work"},
		{"", ""},
	} {
		if got := actorSlug(tc.in); got != tc.want {
			t.Errorf("actorSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// assertStoreActor reads the store's own config.yml, because that file is what
// git-ticket falls back to when a command names no actor. An assertion against
// the tool's own return value would prove only that the tool agrees with
// itself.
func assertStoreActor(t *testing.T, dir, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".tickets", "config.yml"))
	if err != nil {
		t.Fatalf("read the store config: %v", err)
	}
	got := string(b)
	if want == "" {
		if strings.Contains(got, "id:") {
			t.Errorf("the store names an actor though none was chosen:\n%s", got)
		}
		return
	}
	if !strings.Contains(got, want) {
		t.Errorf("the store does not record %q:\n%s", want, got)
	}
}
