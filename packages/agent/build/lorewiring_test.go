package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The /context size probe uses PerTurnContextPeek, which must render the same
// tail as PerTurnContext but NOT record which lore fired — otherwise opening
// /context re-scans the (now longer) transcript and /lore reports lore that was
// never actually injected.
func TestPerTurnContextPeek_DoesNotRecord(t *testing.T) {
	entry := lore.Entry{Name: "vault", Keys: []string{"vault"}, Content: "The vault is sealed.", Order: 1, Source: "vault.md"}
	r := &Resolved{
		loreTriggered: []lore.Entry{entry},
		loreConfig:    lore.Config{},
		loreFired:     &LoreFiredRecord{},
	}
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "tell me about the vault"}}},
	})

	peek := r.PerTurnContextPeek(ag)
	if peek == nil {
		t.Fatal("peek should be non-nil when lore is present")
	}
	if s := peek(); !strings.Contains(s, "vault is sealed") {
		t.Fatalf("peek should render the fired lore, got %q", s)
	}
	if got := r.loreFired.Get(); len(got) != 0 {
		t.Errorf("peek must NOT record fired lore; loreFired = %v", got)
	}

	// The real provider does record, for /lore's "fired last turn" — including WHY
	// (the matched key).
	if s := r.PerTurnContext(ag)(); !strings.Contains(s, "vault is sealed") {
		t.Fatalf("provider should render the fired lore, got %q", s)
	}
	got := r.loreFired.Get()
	if len(got) != 1 || got[0].Source != "vault.md" || got[0].Dropped {
		t.Fatalf("provider should record the fired entry, got %+v", got)
	}
	if len(got[0].Keys) != 1 || got[0].Keys[0] != "vault" {
		t.Errorf("provider should record the matched key, got %v", got[0].Keys)
	}
}

// WireExtEphemeral must (a) compose BOTH the live provider and the sizing
// twin — the interactive path once composed only the live one, so /context
// undercounted the tail whenever lore was active alongside an extension —
// and (b) place extension context BEFORE the run's tail, so a card's
// post_history_instructions stays last.
func TestWireExtEphemeral_PeekInSyncAndPHILast(t *testing.T) {
	tail := func() string { return "triggered lore\n\nStay terse. (PHI)" }
	ag := &core.Agent{}
	ag.ContextProvider = tail
	ag.ContextProviderPeek = tail
	WireExtEphemeral(ag, func() string { return "ext: world state" })

	want := "ext: world state\n\ntriggered lore\n\nStay terse. (PHI)"
	if got := ag.ContextProvider(); got != want {
		t.Errorf("live tail = %q, want ext context first / PHI last", got)
	}
	if got := ag.ContextProviderPeek(); got != want {
		t.Errorf("peek tail = %q, must match the live composition", got)
	}

	// A run with no tail of its own still gets the ext context on both.
	bare := &core.Agent{}
	WireExtEphemeral(bare, func() string { return "ext only" })
	if bare.ContextProvider() != "ext only" || bare.ContextProviderPeek() != "ext only" {
		t.Error("ext context should stand alone when the run has no tail")
	}
}

func TestLoreFiredRecord(t *testing.T) {
	rec := &LoreFiredRecord{}
	if got := rec.Get(); len(got) != 0 {
		t.Errorf("fresh record should be empty, got %v", got)
	}
	rec.Set([]LoreFired{
		{Name: "vault", Source: "vault.md", Keys: []string{"vault"}},
		{Name: "minor", Source: "minor.md", Dropped: true},
	})
	got := rec.Get()
	if len(got) != 2 || got[0].Source != "vault.md" || got[0].Dropped {
		t.Fatalf("Get after set = %+v", got)
	}
	if !got[1].Dropped || got[1].Source != "minor.md" {
		t.Fatalf("dropped entry not recorded: %+v", got)
	}
	// Get returns a defensive copy — mutating it must not affect the record.
	got[0].Source = "mutated"
	if rec.Get()[0].Source != "vault.md" {
		t.Error("Get should return a copy, not the backing slice")
	}
}

// Budget overflow must never truncate silently (the proposal's explicit
// rule): the entries the budget drops are recorded alongside what fired, so
// /lore can surface them.
func TestPerTurnContext_RecordsBudgetDropped(t *testing.T) {
	// Two entries both fire on "vault"; a tiny budget keeps only the
	// higher-priority one (the top entry is always kept).
	big := lore.Entry{Name: "big", Keys: []string{"vault"}, Content: strings.Repeat("sealed history ", 200), Order: 10, Source: "big.md"}
	small := lore.Entry{Name: "small", Keys: []string{"vault"}, Content: strings.Repeat("side note ", 200), Order: 1, Source: "small.md"}
	r := &Resolved{
		loreTriggered: []lore.Entry{big, small},
		loreConfig:    lore.Config{TokenBudget: 10},
		loreFired:     &LoreFiredRecord{},
	}
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "open the vault"}}},
	})

	if s := r.PerTurnContext(ag)(); !strings.Contains(s, "sealed history") {
		t.Fatalf("the top-priority entry should still inject, got %q", s[:min(len(s), 80)])
	}
	trace := r.loreFired.Get()
	if len(trace) != 2 {
		t.Fatalf("both activated entries should be traced, got %+v", trace)
	}
	var bigF, smallF LoreFired
	for _, f := range trace {
		switch f.Source {
		case "big.md":
			bigF = f
		case "small.md":
			smallF = f
		}
	}
	if bigF.Source != "big.md" || bigF.Dropped {
		t.Errorf("big should be kept and traced, got %+v", bigF)
	}
	if smallF.Source != "small.md" || !smallF.Dropped {
		t.Errorf("small should be recorded as dropped for budget (no silent truncation), got %+v", smallF)
	}
}

// World lore (Worlds L1) rides the same per-turn Select as file/card lore:
// constants inject every turn, keyed entries fire on recent-message keywords,
// and a live edit (world.lore.put) is visible on the next call with no rebuild
// — the reason World constants stay OUT of the cached prefix.
func TestPerTurnContext_WorldLore(t *testing.T) {
	// DefaultOrder on the file entry mirrors ParseEntry's default — the
	// equal-order case the placement rule below is about.
	r := &Resolved{
		loreTriggered: []lore.Entry{{Name: "vault", Keys: []string{"vault"}, Content: "The vault is sealed.", Order: lore.DefaultOrder, Source: "vault.md"}},
		loreFired:     &LoreFiredRecord{},
		worldLore:     &WorldLoreRecord{},
	}
	r.worldLore.Set(WorldLoreEntries([]core.WorldLoreEntry{
		{Name: "The Accord", Constant: true, Content: "Magic is outlawed in the city."},
		{Name: "Elira's debt", Keys: []string{"debt"}, Content: "Elira owes the guild a life."},
	}))
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "we approach the vault"}}},
	})

	fn := r.PerTurnContext(ag)
	if fn == nil {
		t.Fatal("expected a live tail with world lore present")
	}
	got := fn()
	if !strings.Contains(got, "Magic is outlawed") {
		t.Errorf("a constant world entry must inject every turn, got %q", got)
	}
	if !strings.Contains(got, "vault is sealed") {
		t.Errorf("card/file lore must still fire alongside world lore, got %q", got)
	}
	if strings.Contains(got, "owes the guild") {
		t.Errorf("an un-triggered keyed world entry must not inject, got %q", got)
	}
	// At equal Order the world's shared facts lead the card's book (stable sort,
	// world entries first in the Select input).
	if strings.Index(got, "Magic is outlawed") > strings.Index(got, "vault is sealed") {
		t.Errorf("world lore should place before card/file lore, got %q", got)
	}
	// The activation trace tags world entries with Source "world"; a constant
	// fires with no keys.
	var world LoreFired
	for _, f := range r.loreFired.Get() {
		if f.Source == "world" {
			world = f
		}
	}
	if world.Name != "The Accord" || !world.Constant || len(world.Keys) != 0 {
		t.Errorf("constant world entry trace = %+v", world)
	}

	// The keyed entry fires once its keyword enters the scan window.
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "what about her debt?"}}},
	})
	if got := fn(); !strings.Contains(got, "owes the guild") {
		t.Errorf("keyed world entry should fire on its keyword, got %q", got)
	}

	// A live edit lands on the next call — no rebuild, no cache bust.
	r.worldLore.Set(WorldLoreEntries([]core.WorldLoreEntry{
		{Name: "The Accord", Constant: true, Content: "The Accord has fallen."},
	}))
	if got := fn(); !strings.Contains(got, "Accord has fallen") || strings.Contains(got, "Magic is outlawed") {
		t.Errorf("live world-lore edit not reflected: %q", got)
	}

	// A bare world record keeps the tail live (an entry added later injects),
	// exactly like a note record.
	empty := &Resolved{worldLore: &WorldLoreRecord{}, loreFired: &LoreFiredRecord{}}
	fn2 := empty.PerTurnContext(&core.Agent{})
	if fn2 == nil {
		t.Fatal("a world-lore record must keep the tail live even with no lore/PHI/note")
	}
	if got := fn2(); got != "" {
		t.Errorf("empty world lore should render nothing, got %q", got)
	}
	empty.worldLore.Set(WorldLoreEntries([]core.WorldLoreEntry{{Name: "Seed", Constant: true, Content: "It begins."}}))
	if got := fn2(); !strings.Contains(got, "It begins.") {
		t.Errorf("world lore added after wiring should inject: %q", got)
	}
}

// The pinned scene-state card (SD4) bypasses the lore Select entirely: it
// renders as its own framed block AFTER the <lore> block, outside the token
// budget — a long session is exactly when the budget is tightest and exactly
// when the pinned clock/ledger matter most, so "constant but budget-droppable"
// would fail it precisely on demand.
func TestPerTurnContext_SceneStatePinned(t *testing.T) {
	// A tiny budget that a peer constant entry overflows: the peer is dropped,
	// the pin is not — it never entered the Select.
	r := &Resolved{
		loreConfig: lore.Config{TokenBudget: 10},
		loreFired:  &LoreFiredRecord{},
		worldLore:  &WorldLoreRecord{},
		note:       &NoteRecord{},
	}
	r.worldLore.Set(WorldLoreEntries([]core.WorldLoreEntry{
		{Name: "The Accord", Constant: true, Content: strings.Repeat("Magic is outlawed. ", 200)},
		{Name: core.SceneStateName, Constant: true, Content: "Day 14, first light. The Hearthstone Inn. 3 silver owed."},
		{Name: "Overflow", Constant: true, Content: strings.Repeat("side note ", 200)},
	}))
	r.note.Set("It is raining.")
	got := r.PerTurnContext(&core.Agent{})()

	if !strings.Contains(got, "CURRENT SCENE STATE") || !strings.Contains(got, "Day 14, first light") {
		t.Fatalf("the scene-state card must render as a pinned block, got %q", got)
	}
	// Outside the <lore> block: the pin sits after </lore>, before the note.
	if end := strings.Index(got, "</lore>"); end < 0 || strings.Index(got, "CURRENT SCENE STATE") < end {
		t.Errorf("the pinned card must render outside (after) the lore block, got %q", got)
	}
	if strings.Index(got, "CURRENT SCENE STATE") > strings.Index(got, "It is raining.") {
		t.Errorf("the author's note must still come last, got %q", got)
	}
	// The budget dropped a peer constant — but never the pin.
	dropped := false
	for _, f := range r.loreFired.Get() {
		if f.Dropped {
			dropped = true
		}
		if f.Name == core.SceneStateName {
			t.Errorf("the pin must not ride the Select/budget at all, trace = %+v", f)
		}
	}
	if !dropped {
		t.Fatal("test setup: the budget should have dropped a peer entry (else the immunity claim is untested)")
	}

	// A live update (world_note / world.lore.put) lands next turn, like all
	// world lore.
	r.worldLore.Set(WorldLoreEntries([]core.WorldLoreEntry{
		{Name: core.SceneStateName, Constant: true, Content: "Day 15, dusk. The north road."},
	}))
	if got := r.PerTurnContext(&core.Agent{})(); !strings.Contains(got, "Day 15, dusk") || strings.Contains(got, "Day 14") {
		t.Errorf("live scene-state update not reflected: %q", got)
	}

	// A world record holding ONLY the pin still renders it (no other lore to
	// carry the Select path).
	solo := &Resolved{worldLore: &WorldLoreRecord{}, loreFired: &LoreFiredRecord{}}
	solo.worldLore.Set(WorldLoreEntries([]core.WorldLoreEntry{
		{Name: "scene state", Constant: true, Content: "Midnight, the docks."}, // case-insensitive match
	}))
	if got := solo.PerTurnContext(&core.Agent{})(); !strings.Contains(got, "CURRENT SCENE STATE") || !strings.Contains(got, "Midnight, the docks.") || strings.Contains(got, "<lore>") {
		t.Errorf("a pin-only world must render the card and no lore block, got %q", got)
	}
}

// Lore rides the tail AFTER the whole transcript, so an entry written to set a
// scene up is the last thing the model reads long after the scene has played
// past it. A dogfooded session lost a beat exactly this way: a setup entry said
// the lieutenant was expected at the door and should be asked her team size;
// she had been let in eight messages earlier; the model re-staged her arrival.
// The frame is the fix — this block is reference, and the transcript wins.
func TestPerTurnContext_LoreFramedAsReference(t *testing.T) {
	r := &Resolved{loreFired: &LoreFiredRecord{}, worldLore: &WorldLoreRecord{}}
	r.worldLore.Set(WorldLoreEntries([]core.WorldLoreEntry{
		{Name: "First-Light Search", Keys: []string{"rope"}, Content: "The watch is expected at first light."},
	}))
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hand me the rope"}}},
	})
	got := r.PerTurnContext(ag)()

	if !strings.Contains(got, "REFERENCE KNOWLEDGE") {
		t.Fatalf("the tail lore block must be framed as reference, got %q", got)
	}
	// The frame introduces the block, so it must precede it.
	if strings.Index(got, "REFERENCE KNOWLEDGE") > strings.Index(got, "<lore>") {
		t.Errorf("the frame must introduce the lore block, got %q", got)
	}
	// The tiebreak is the whole point: without it the block reads as the current
	// moment purely because it lands last.
	if !strings.Contains(got, "the conversation is what actually happened") {
		t.Errorf("the frame must hand the tiebreak to the transcript, got %q", got)
	}

	// The cached system prefix sits BEFORE the conversation, where the frame's
	// "the conversation above" would name nothing — it must stay unframed.
	prefix := lore.PlaceConstant([]lore.Entry{{Name: "House", Constant: true, Content: "House style."}})
	if prefix == "" || strings.Contains(prefix, "REFERENCE KNOWLEDGE") {
		t.Errorf("the cached prefix must not carry the tail frame, got %q", prefix)
	}
}

func TestLoreSourcesOf(t *testing.T) {
	srcs := loreSourcesOf([]lore.Entry{{Source: "vault.md"}, {Name: "NoSource"}})
	if len(srcs) != 2 || srcs[0] != "vault.md" || srcs[1] != "NoSource" {
		t.Errorf("sources (source-or-name fallback) = %v", srcs)
	}
}
