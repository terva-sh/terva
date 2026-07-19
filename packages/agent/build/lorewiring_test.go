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

func TestLoreSourcesOf(t *testing.T) {
	srcs := loreSourcesOf([]lore.Entry{{Source: "vault.md"}, {Name: "NoSource"}})
	if len(srcs) != 2 || srcs[0] != "vault.md" || srcs[1] != "NoSource" {
		t.Errorf("sources (source-or-name fallback) = %v", srcs)
	}
}
