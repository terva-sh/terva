package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/provider"
)

func memTool() *MemoryTool {
	return &MemoryTool{
		Project: memory.NewStore(), User: memory.NewUserStore(),
		ProjectArchive: memory.NewArchive(memory.ScopeProject, memory.LabelProject),
		UserArchive:    memory.NewArchive(memory.ScopeUser, memory.LabelUser),
	}
}

// call runs one memory tool action and returns its text plus whether it was
// reported as an error.
func call(t *testing.T, mt *MemoryTool, args map[string]any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := mt.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute(%v): %v", args, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String(), res.IsError
}

// Moving an active entry into the archive must leave it in exactly one tier.
// Both failure directions cost something real: still in both means paying prefix
// bytes for a memory that was supposed to stop costing them, and in neither
// means the operation deleted a fact while reporting success.
func TestArchivingAnActiveEntryMovesItExactlyOnce(t *testing.T) {
	mt := memTool()
	if err := mt.Project.Add("release tags cannot be reused once burned"); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, mt, map[string]any{
		"action": "archive", "match": "burned", "name": "burned tags",
		"keys": []string{"release", "tag"},
	})
	if isErr {
		t.Fatalf("archive failed: %s", out)
	}
	if got := mt.Project.List(); len(got) != 0 {
		t.Errorf("the entry is still in active memory as well: %v", got)
	}
	if n := mt.ProjectArchive.Len(); n != 1 {
		t.Fatalf("archive holds %d entries, want 1", n)
	}
	if !strings.Contains(out, "Removed from active memory") {
		t.Errorf("the reply does not say the entry moved:\n%s", out)
	}
	// The reply names the triggers, which is the only cheap way a curator
	// notices a spec keyed on the wrong vocabulary — the failure is otherwise
	// silent by construction.
	if !strings.Contains(out, "release") {
		t.Errorf("the reply withholds the entry's triggers:\n%s", out)
	}
}

// An archive that cannot be written must not remove the active entry. The order
// is the only thing enforcing it, so this pins the order by breaking the write:
// an entry with no keys is refused, and the active copy has to survive that.
func TestAFailedArchiveLeavesTheActiveEntryAlone(t *testing.T) {
	mt := memTool()
	if err := mt.Project.Add("the build command is just ci"); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, mt, map[string]any{"action": "archive", "match": "build command"})
	if !isErr {
		t.Fatalf("archiving with no keys was accepted: %s", out)
	}
	if got := mt.Project.List(); len(got) != 1 {
		t.Fatalf("a failed archive destroyed the active entry: %v", got)
	}
	if mt.ProjectArchive.Len() != 0 {
		t.Error("a refused archive stored something anyway")
	}
}

// Promotion is the expensive recall verb, and it goes through the active tier's
// caps. A promotion the caps refuse must leave the entry ARCHIVED — the entry
// exists in one place before the call and must exist in one place after it.
func TestAPromotionTheActiveCapsRefuseLeavesTheEntryArchived(t *testing.T) {
	mt := memTool()
	long := strings.Repeat("x", memory.MaxEntryLen+1)
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "a long procedure", Keys: []string{"procedure"}, Text: long,
	}); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, mt, map[string]any{"action": "promote", "match": "a-long-procedure"})
	if !isErr {
		t.Fatalf("an over-length entry was promoted into the one-line tier: %s", out)
	}
	if mt.ProjectArchive.Len() != 1 {
		t.Fatal("the refused promotion deleted the archived entry")
	}
	if got := mt.Project.List(); len(got) != 0 {
		t.Errorf("a truncated copy reached active memory: %v", got)
	}
	if !strings.Contains(out, "still archived") {
		t.Errorf("the refusal does not say where the entry is now:\n%s", out)
	}
}

// The happy path of promotion: out of the archive, into the active tier, once.
func TestPromotionMovesAnEntryIntoTheActiveTier(t *testing.T) {
	mt := memTool()
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "the gate", Keys: []string{"ci"}, Text: "CI runs go test -race; just ci does not.",
	}); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, mt, map[string]any{"action": "promote", "match": "the-gate"})
	if isErr {
		t.Fatalf("promote failed: %s", out)
	}
	if mt.ProjectArchive.Len() != 0 {
		t.Error("the archived copy survived promotion; the entry is now in both tiers")
	}
	got := mt.Project.List()
	if len(got) != 1 || !strings.Contains(got[0], "go test -race") {
		t.Fatalf("active memory after promotion = %v", got)
	}
	if !strings.Contains(out, "go test -race") {
		t.Errorf("the reply does not show the updated active tier:\n%s", out)
	}
}

// recall reads an archived entry without moving it — the cheap escape hatch,
// against promote's cached-prefix invalidation. It must not change either tier.
func TestRecallReadsWithoutMoving(t *testing.T) {
	mt := memTool()
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "nightly", Keys: []string{"backup"}, Text: "Backups run nightly at 02:00 UTC.",
	}); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, mt, map[string]any{"action": "recall", "match": "nightly"})
	if isErr {
		t.Fatalf("recall failed: %s", out)
	}
	if !strings.Contains(out, "02:00 UTC") {
		t.Errorf("recall did not return the body:\n%s", out)
	}
	if mt.ProjectArchive.Len() != 1 || len(mt.Project.List()) != 0 {
		t.Error("recall moved the entry; it is meant to read it in place")
	}
}

// Scope resolution has to agree across the two tiers. The archive actions
// resolve the store and the archive in separate calls, so a scope that means
// "project" to one and something else to the other would move an entry between
// scopes without saying so.
func TestArchiveActionsHonourTheUserScope(t *testing.T) {
	mt := memTool()
	if err := mt.User.Add("prefers worktrees over branch switching"); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, mt, map[string]any{
		"action": "archive", "scope": memory.ScopeUser, "match": "worktrees",
		"name": "worktree habit", "keys": []string{"worktree"},
	})
	if isErr {
		t.Fatalf("archive to the user scope failed: %s", out)
	}
	if mt.UserArchive.Len() != 1 {
		t.Errorf("the user archive holds %d entries, want 1", mt.UserArchive.Len())
	}
	if mt.ProjectArchive.Len() != 0 {
		t.Error("a user-scoped archive landed in the project archive")
	}
	if len(mt.User.List()) != 0 {
		t.Error("the user's active entry was not removed after moving")
	}
	if !strings.Contains(out, "[user:worktree-habit]") {
		t.Errorf("the reply does not name the entry by scope and id:\n%s", out)
	}
}

// An unbound archive (memory available, no resolvable target) must refuse
// clearly rather than accepting writes that go nowhere.
func TestArchiveActionsRefuseWithNoArchiveBound(t *testing.T) {
	mt := &MemoryTool{Project: memory.NewStore(), User: memory.NewUserStore()}
	for _, action := range []string{"archive", "search", "recall", "promote", "forget"} {
		out, isErr := call(t, mt, map[string]any{"action": action, "text": "x", "match": "x", "keys": []string{"k"}})
		if !isErr {
			t.Errorf("%s succeeded with no archive bound: %s", action, out)
		}
		if !strings.Contains(out, "archive") {
			t.Errorf("%s: refusal does not name what is missing: %s", action, out)
		}
	}
	// The active tier is unaffected — the archive being absent must not take
	// ordinary memory down with it.
	if out, isErr := call(t, mt, map[string]any{"action": "add", "text": "still works"}); isErr {
		t.Errorf("the active tier broke when the archive was absent: %s", out)
	}
}

// An unknown action names every verb rather than only the ones it happens to
// remember, so a model that guessed can recover in the same turn.
func TestAnUnknownActionListsEveryVerb(t *testing.T) {
	mt := memTool()
	out, isErr := call(t, mt, map[string]any{"action": "reticulate"})
	if !isErr {
		t.Fatal("an unknown action succeeded")
	}
	for _, verb := range []string{"add", "replace", "remove", "archive", "search", "recall", "promote", "forget"} {
		if !strings.Contains(out, verb) {
			t.Errorf("the refusal omits %q:\n%s", verb, out)
		}
	}
}

// The schema's action enum and the dispatch must not drift: an action the schema
// advertises but Execute rejects is a tool the model cannot use correctly, and
// the model only ever sees the schema.
func TestEveryAdvertisedActionIsDispatched(t *testing.T) {
	var schema struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	mt := memTool()
	if err := json.Unmarshal(mt.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties.Action.Enum) == 0 {
		t.Fatal("the schema advertises no actions; this guard would pass vacuously")
	}
	for _, action := range schema.Properties.Action.Enum {
		out, _ := call(t, memTool(), map[string]any{"action": action})
		if strings.Contains(out, "action must be one of") || strings.Contains(out, "unknown archive action") {
			t.Errorf("schema advertises %q but Execute does not dispatch it: %s", action, out)
		}
	}
}

// Details ride every reply so a surface can show what happened without parsing
// prose. Checked on both tiers because they are built by different helpers.
func TestRepliesCarryStructuredDetails(t *testing.T) {
	mt := memTool()
	run := func(args map[string]any) map[string]any {
		raw, _ := json.Marshal(args)
		res, err := mt.Execute(context.Background(), raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		d, ok := res.Details.(map[string]any)
		if !ok {
			t.Fatalf("Details = %#v, want a map a surface can read", res.Details)
		}
		return d
	}
	if d := run(map[string]any{"action": "add", "text": "a fact"}); d["entries"] != 1 {
		t.Errorf("active reply details = %v", d)
	}
	if d := run(map[string]any{"action": "archive", "text": "a longer fact", "keys": []string{"fact"}}); d["archived"] != 1 {
		t.Errorf("archive reply details = %v", d)
	}
}
