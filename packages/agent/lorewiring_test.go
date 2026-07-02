package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The /context size probe uses perTurnContextPeek, which must render the same
// tail as perTurnContext but NOT record which lore fired — otherwise opening
// /context re-scans the (now longer) transcript and /lore reports lore that was
// never actually injected.
func TestPerTurnContextPeek_DoesNotRecord(t *testing.T) {
	entry := lore.Entry{Name: "vault", Keys: []string{"vault"}, Content: "The vault is sealed.", Order: 1, Source: "vault.md"}
	r := &Resolved{
		loreTriggered: []lore.Entry{entry},
		loreConfig:    lore.Config{},
		loreFired:     &loreFiredRecord{},
	}
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "tell me about the vault"}}},
	})

	peek := r.perTurnContextPeek(ag)
	if peek == nil {
		t.Fatal("peek should be non-nil when lore is present")
	}
	if s := peek(); !strings.Contains(s, "vault is sealed") {
		t.Fatalf("peek should render the fired lore, got %q", s)
	}
	if got := r.loreFired.get(); len(got) != 0 {
		t.Errorf("peek must NOT record fired lore; loreFired = %v", got)
	}

	// The real provider does record, for /lore's "fired last turn".
	if s := r.perTurnContext(ag)(); !strings.Contains(s, "vault is sealed") {
		t.Fatalf("provider should render the fired lore, got %q", s)
	}
	if got := r.loreFired.get(); len(got) != 1 || got[0] != "vault.md" {
		t.Errorf("provider should record fired lore, got %v", got)
	}
}

// wireExtEphemeral must (a) compose BOTH the live provider and the sizing
// twin — the interactive path once composed only the live one, so /context
// undercounted the tail whenever lore was active alongside an extension —
// and (b) place extension context BEFORE the run's tail, so a card's
// post_history_instructions stays last.
func TestWireExtEphemeral_PeekInSyncAndPHILast(t *testing.T) {
	tail := func() string { return "triggered lore\n\nStay terse. (PHI)" }
	ag := &core.Agent{}
	ag.ContextProvider = tail
	ag.ContextProviderPeek = tail
	wireExtEphemeral(ag, func() string { return "ext: world state" })

	want := "ext: world state\n\ntriggered lore\n\nStay terse. (PHI)"
	if got := ag.ContextProvider(); got != want {
		t.Errorf("live tail = %q, want ext context first / PHI last", got)
	}
	if got := ag.ContextProviderPeek(); got != want {
		t.Errorf("peek tail = %q, must match the live composition", got)
	}

	// A run with no tail of its own still gets the ext context on both.
	bare := &core.Agent{}
	wireExtEphemeral(bare, func() string { return "ext only" })
	if bare.ContextProvider() != "ext only" || bare.ContextProviderPeek() != "ext only" {
		t.Error("ext context should stand alone when the run has no tail")
	}
}

func TestLoreFiredRecord(t *testing.T) {
	rec := &loreFiredRecord{}
	if got := rec.get(); len(got) != 0 {
		t.Errorf("fresh record should be empty, got %v", got)
	}
	rec.set([]string{"vault.md", "auth.md"}, []string{"minor.md"})
	got := rec.get()
	if len(got) != 2 || got[0] != "vault.md" {
		t.Fatalf("get after set = %v", got)
	}
	if d := rec.getDropped(); len(d) != 1 || d[0] != "minor.md" {
		t.Fatalf("getDropped after set = %v", d)
	}
	// get returns a defensive copy — mutating it must not affect the record.
	got[0] = "mutated"
	if rec.get()[0] != "vault.md" {
		t.Error("get should return a copy, not the backing slice")
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
		loreFired:     &loreFiredRecord{},
	}
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "open the vault"}}},
	})

	if s := r.perTurnContext(ag)(); !strings.Contains(s, "sealed history") {
		t.Fatalf("the top-priority entry should still inject, got %q", s[:min(len(s), 80)])
	}
	if fired := r.loreFired.get(); len(fired) != 1 || fired[0] != "big.md" {
		t.Errorf("fired = %v, want just big.md", fired)
	}
	if dropped := r.loreFired.getDropped(); len(dropped) != 1 || dropped[0] != "small.md" {
		t.Errorf("dropped = %v, want small.md recorded (no silent truncation)", dropped)
	}
}

func TestLoreSourcesOf(t *testing.T) {
	srcs := loreSourcesOf([]lore.Entry{{Source: "vault.md"}, {Name: "NoSource"}})
	if len(srcs) != 2 || srcs[0] != "vault.md" || srcs[1] != "NoSource" {
		t.Errorf("sources (source-or-name fallback) = %v", srcs)
	}
}
