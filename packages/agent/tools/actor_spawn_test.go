package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// fakeActor drives the turn coordination without a live subprocess. When
// fireOnSet is true it simulates a turn that already ended by the time the
// watcher is installed (the buffered turnDone channel must still deliver).
type fakeActor struct {
	fireOnSet bool
	err       error
}

func (f *fakeActor) SetOnTurnEnd(fn func(int, string)) {
	if f.fireOnSet {
		fn(0, "")
	}
}
func (f *fakeActor) Err() error { return f.err }

func TestWaitTurn_TurnEnd(t *testing.T) {
	fa := &fakeActor{fireOnSet: true}
	turnDone := installTurnWatcher(fa)
	if err := waitTurn(context.Background(), turnDone, make(chan struct{}), fa); err != nil {
		t.Fatal(err)
	}
}

func TestWaitTurn_CrashBeforeLine(t *testing.T) {
	terminated := make(chan struct{})
	close(terminated) // the actor terminated without finishing a turn
	fa := &fakeActor{err: errors.New("boom")}
	turnDone := installTurnWatcher(fa)
	if err := waitTurn(context.Background(), turnDone, terminated, fa); err == nil || !strings.Contains(err.Error(), "exited before responding") {
		t.Errorf("want an exit error, got %v", err)
	}
}

func TestWaitTurn_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fa := &fakeActor{}
	turnDone := installTurnWatcher(fa)
	if err := waitTurn(ctx, turnDone, make(chan struct{}), fa); err == nil {
		t.Fatal("want a context error")
	}
}

// The reply is the LAST assistant message — not the seeded greeting, and not the
// echoed situation user turn.
func TestLastAssistantText(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "greeting (first_mes)"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "the situation"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Shut the door behind you."}}},
	}
	if got := lastAssistantText(msgs); got != "Shut the door behind you." {
		t.Errorf("lastAssistantText = %q, want the reply (not the greeting)", got)
	}
}

// An empty newest reply must yield "" (so the tool reports "produced no
// line"), never a stale line from before the last user turn: on the warm
// path that would replay the actor's previous line as if new, and on first
// appearance it would replay the seeded greeting.
func TestLastAssistantText_EmptyReplyIsNotStaleHistory(t *testing.T) {
	greeting := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "greeting (first_mes)"}}}
	turn1User := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "situation 1"}}}
	turn1Reply := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "An earlier line."}}}
	turn2User := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "situation 2"}}}

	// Whitespace-only newest reply: committed but blank.
	blank := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "  \n\t"}}}
	if got := lastAssistantText([]provider.Message{greeting, turn1User, turn1Reply, turn2User, blank}); got != "" {
		t.Errorf("whitespace-only reply should yield \"\", got %q (stale line)", got)
	}
	// Fully empty turn: nothing committed after the situation.
	if got := lastAssistantText([]provider.Message{greeting, turn1User, turn1Reply, turn2User}); got != "" {
		t.Errorf("no reply after the last user turn should yield \"\", got %q (stale line)", got)
	}
	// Sanity: a real newest reply still wins.
	real := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "A fresh line."}}}
	if got := lastAssistantText([]provider.Message{greeting, turn1User, turn1Reply, turn2User, real}); got != "A fresh line." {
		t.Errorf("lastAssistantText = %q, want the fresh reply", got)
	}
}

func TestActorReply_TranscriptFallback(t *testing.T) {
	line := actorReply("", []string{"user: the situation", "Fog's early tonight.", "Which is it?"})
	if line != "Fog's early tonight.\nWhich is it?" {
		t.Errorf("fallback line = %q", line)
	}
}

func TestComposeActorTask(t *testing.T) {
	got := composeActorTask("Aava hears wet boots on the stairs.")
	if !strings.Contains(got, "Aava hears wet boots on the stairs.") {
		t.Error("composed task should contain the situation")
	}
	if !strings.Contains(got, "voicing a character") || !strings.Contains(got, "narrator owns the world") {
		t.Errorf("composed task should carry the actor framing note: %q", got)
	}
}

// WarmActors is a pure LRU cache: reuse marks most-recently-used, and putting
// over capacity evicts the least-recently-used.
func TestWarmActors_LRU(t *testing.T) {
	w := NewWarmActors(2)
	a, b, c := &warmActor{}, &warmActor{}, &warmActor{}

	if evName, ev := w.put("a", a); ev != nil {
		t.Fatalf("put under cap should not evict, got %q", evName)
	}
	w.put("b", b)
	// Touch "a" so "b" becomes the least-recently-used.
	if got := w.get("a"); got != a {
		t.Fatal("get(a) should return the live actor a")
	}
	// Adding "c" exceeds cap 2 → evict the LRU, which is now "b".
	evName, ev := w.put("c", c)
	if evName != "b" || ev != b {
		t.Fatalf("expected to evict b, got %q/%v", evName, ev)
	}
	if w.get("b") != nil {
		t.Error("b should be gone after eviction")
	}
	if w.get("a") != a || w.get("c") != c {
		t.Error("a and c should still be live")
	}
}

// Re-putting a name that is already live must hand back the displaced actor
// for retirement — a silent map overwrite would leak its subprocess and
// terminated-watcher goroutine. Unreachable via acquire today (it get()s or
// drop()s first); this pins the contract for any future caller.
func TestWarmActors_RePutDisplacesLiveActor(t *testing.T) {
	w := NewWarmActors(2)
	a1, a2 := &warmActor{}, &warmActor{}
	w.put("a", a1)
	name, displaced := w.put("a", a2)
	if name != "a" || displaced != a1 {
		t.Fatalf("re-put should displace the live actor: got %q/%v", name, displaced)
	}
	if w.get("a") != a2 {
		t.Error("the new actor should be live under the name")
	}
	// The order slice must not carry a duplicate: filling to cap evicts
	// nothing, and one over cap evicts the true LRU.
	if _, ev := w.put("b", &warmActor{}); ev != nil {
		t.Error("filling to cap must not evict")
	}
	if evName, _ := w.put("c", &warmActor{}); evName != "a" {
		t.Errorf("over cap should evict the LRU (a), got %q", evName)
	}
}

func TestWarmActors_DropAndShutdown(t *testing.T) {
	w := NewWarmActors(5)
	a := &warmActor{agent: &swarm.Agent{ID: "agent-a"}}
	w.put("a", a)

	if got := w.drop("a"); got != a {
		t.Fatalf("drop(a) = %v, want a", got)
	}
	if w.drop("a") != nil {
		t.Error("dropping again should be nil")
	}

	b := &warmActor{agent: &swarm.Agent{ID: "agent-b"}}
	w.put("b", b)
	var stopped []string
	w.Shutdown(func(id string) { stopped = append(stopped, id) })
	if len(stopped) != 1 || stopped[0] != "agent-b" {
		t.Errorf("Shutdown should stop the live actor, got %v", stopped)
	}
	if w.get("b") != nil {
		t.Error("cache should be empty after Shutdown")
	}
}

// fakeEngine records the swarm calls acquire makes so reuse is provable without
// a live subprocess.
type fakeEngine struct {
	spawns  int
	sends   int
	stops   int
	sendErr error
}

func (f *fakeEngine) SpawnReq(ctx context.Context, req swarm.SpawnRequest) (*swarm.Agent, error) {
	f.spawns++
	return &swarm.Agent{ID: "agent-" + string(rune('0'+f.spawns))}, nil
}
func (f *fakeEngine) SendUserTurn(id, text string) error { f.sends++; return f.sendErr }
func (f *fakeEngine) Stop(id string) error               { f.stops++; return nil }
func (f *fakeEngine) Remove(id string) error             { return nil }

// The load-bearing claim of warm actors: the SECOND dispatch of the same actor
// reuses the live one (SendUserTurn) instead of spawning again.
func TestActorSpawn_ReusesWarmActor(t *testing.T) {
	fake := &fakeEngine{}
	tool := &ActorSpawnTool{Swarm: fake, Warm: NewWarmActors(5), Cast: map[string]CastMember{"aava": {Persona: "aava"}}}
	member := tool.Cast["aava"]

	wa1, _, err := tool.acquire(context.Background(), "aava", "situation 1", member, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.spawns != 1 || fake.sends != 0 {
		t.Fatalf("first dispatch should spawn once: spawns=%d sends=%d", fake.spawns, fake.sends)
	}

	wa2, _, err := tool.acquire(context.Background(), "aava", "situation 2", member, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.spawns != 1 {
		t.Errorf("second dispatch must REUSE, not spawn again: spawns=%d", fake.spawns)
	}
	if fake.sends != 1 {
		t.Errorf("second dispatch must SendUserTurn to the warm actor: sends=%d", fake.sends)
	}
	if wa2 != wa1 {
		t.Error("reuse should return the same warm actor")
	}
}

// A warm actor whose child has died (SendUserTurn fails) is dropped and re-spawned.
func TestActorSpawn_RespawnsStaleActor(t *testing.T) {
	fake := &fakeEngine{}
	tool := &ActorSpawnTool{Swarm: fake, Warm: NewWarmActors(5), Cast: map[string]CastMember{"aava": {Persona: "aava"}}}
	member := tool.Cast["aava"]

	tool.acquire(context.Background(), "aava", "s1", member, "", nil) // spawn #1
	fake.sendErr = errors.New("socket closed")                        // the child is now gone

	if _, _, err := tool.acquire(context.Background(), "aava", "s2", member, "", nil); err != nil {
		t.Fatal(err)
	}
	if fake.sends != 1 {
		t.Errorf("should have attempted one send to the (stale) actor: sends=%d", fake.sends)
	}
	if fake.spawns != 2 {
		t.Errorf("a stale actor should be re-spawned: spawns=%d", fake.spawns)
	}
	if fake.stops == 0 {
		t.Error("the stale actor should have been retired (stop)")
	}
}

func TestActorSpawnSchema_CastEnum(t *testing.T) {
	tool := &ActorSpawnTool{Cast: map[string]CastMember{
		"innkeeper": {Card: "/c/innkeeper.png"},
		"guard":     {Persona: "guard"},
	}}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	actor := schema["properties"].(map[string]any)["actor"].(map[string]any)
	enum := actor["enum"].([]any)
	got := map[string]bool{}
	for _, e := range enum {
		got[e.(string)] = true
	}
	if len(enum) != 2 || !got["innkeeper"] || !got["guard"] {
		t.Errorf("actor enum should be exactly the cast names, got %v", enum)
	}
}

func TestActorSpawn_Validation(t *testing.T) {
	// No cache/cast → unavailable.
	empty := &ActorSpawnTool{}
	if res, _ := empty.Execute(context.Background(), json.RawMessage(`{"actor":"x","situation":"y"}`), nil); !res.IsError {
		t.Error("empty tool should error")
	}

	sw := swarm.New(swarm.Config{Root: testsupport.TempDir(t), RepoRoot: testsupport.TempDir(t)})
	defer sw.StopAll()
	tool := &ActorSpawnTool{Swarm: sw, Warm: NewWarmActors(5), Cast: map[string]CastMember{"innkeeper": {Persona: "innkeeper"}}}

	// Unknown actor is rejected before any spawn (name-only, closed cast).
	if res, _ := tool.Execute(context.Background(), json.RawMessage(`{"actor":"wizard","situation":"hi"}`), nil); !res.IsError {
		t.Error("unknown actor should error")
	}
	// Empty situation is rejected before any spawn.
	if res, _ := tool.Execute(context.Background(), json.RawMessage(`{"actor":"innkeeper","situation":"  "}`), nil); !res.IsError {
		t.Error("empty situation should error")
	}
}

// Worlds W6: the actor's audience-filtered World-lore block rides its task —
// rendered per dispatch, appended after the situation, name substituted into
// the heading. No source or an empty render leaves the task untouched.
func TestActorSpawn_ComposeTaskWorldLore(t *testing.T) {
	bare := &ActorSpawnTool{}
	if got := bare.composeTask("aava", "the door opens"); !strings.Contains(got, "the door opens") || strings.Contains(got, "knows of the world") {
		t.Errorf("no lore source should leave the task bare: %q", got)
	}

	tool := &ActorSpawnTool{WorldLore: func(actor, situation string) string {
		if actor == "aava" && situation == "the door opens" {
			return "- The vault key is hidden in the well."
		}
		return ""
	}}
	got := tool.composeTask("aava", "the door opens")
	if !strings.Contains(got, "What aava knows of the world") {
		t.Errorf("heading should name the actor: %q", got)
	}
	if !strings.Contains(got, "The vault key is hidden in the well.") {
		t.Errorf("lore block should ride the task: %q", got)
	}
	if got := tool.composeTask("rook", "the door opens"); strings.Contains(got, "vault key") {
		t.Errorf("an out-of-audience actor must not receive the block: %q", got)
	}
}
