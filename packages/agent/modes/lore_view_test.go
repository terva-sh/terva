package modes

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// loreCarrier serves both halves /lore reads: the AUTHORED entries on the lore
// surface, and the last turn's activation trace on the context breakdown.
//
// Two surfaces is the whole reason this test exists. The fired/dropped sections
// were dark for releases because /lore only ever asked the lore surface, and a
// comment there asserted the wire carried no per-turn firing — while
// ContextBreakdown.LoreFired carried exactly that, one surface over.
type loreCarrier struct {
	*fakeCarrier
	entries []ctrlproto.LoreEntry
	fired   []ctrlproto.ContextLoreEntry
}

func (c *loreCarrier) Surface(_ context.Context, _, id string) (ctrlproto.Surface, error) {
	if id != "lore" {
		return ctrlproto.Surface{}, nil
	}
	return ctrlproto.Surface{Lore: &ctrlproto.LoreView{Entries: c.entries}}, nil
}

func (c *loreCarrier) Context(context.Context, string) (ctrlproto.ContextBreakdown, error) {
	return ctrlproto.ContextBreakdown{LoreFired: c.fired}, nil
}

func loreBlock(t *testing.T, c *loreCarrier) string {
	t.Helper()
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = c
	i.slashLore()
	i.mu.Lock()
	defer i.mu.Unlock()
	return stripANSICodes(strings.Join(i.helpBlock, "\n"))
}

func TestSlashLoreShowsWhatFiredAndWhatTheBudgetDropped(t *testing.T) {
	c := &loreCarrier{
		fakeCarrier: newFakeCarrier(),
		entries: []ctrlproto.LoreEntry{
			{Name: "the-accord", Keys: []string{"accord"}, Source: "project"},
			{Name: "house-style", Constant: true, Source: "user"},
		},
		fired: []ctrlproto.ContextLoreEntry{
			{Name: "the-accord", Keys: []string{"accord"}, Source: "project"},
			{Name: "house-style", Constant: true, Source: "user"},
			{Name: "long-backstory", Keys: []string{"war"}, Source: "project", Dropped: true},
		},
	}
	got := loreBlock(t, c)

	if !strings.Contains(got, "fired last turn") {
		t.Errorf("the activation trace is missing entirely:\n%s", got)
	}
	if !strings.Contains(got, "the-accord") {
		t.Errorf("a triggered entry is not listed as fired:\n%s", got)
	}
	// The matched key is the difference between "this fired" and "this fired
	// because you said X" — which is the question someone opens /lore with.
	if !strings.Contains(got, "the-accord (accord)") {
		t.Errorf("a triggered entry should name the key that matched:\n%s", got)
	}

	// A dropped entry did NOT reach the model. Folding it in with the fired
	// ones would report the exact silent truncation this trace exists to
	// prevent, so it gets its own section.
	if !strings.Contains(got, "dropped by token budget last turn") {
		t.Errorf("a budget-dropped entry is not reported:\n%s", got)
	}
	firedIdx := strings.Index(got, "fired last turn")
	dropIdx := strings.Index(got, "dropped by token budget")
	if idx := strings.Index(got, "long-backstory"); idx < dropIdx {
		t.Errorf("the dropped entry appears above the dropped heading — it would read as having fired:\n%s", got)
	}
	if firedIdx > dropIdx {
		t.Error("the dropped section precedes the fired one; fired is the common case and reads first")
	}
}

// The sections are absent, not empty, when nothing fired — /lore is opened to
// see what is loaded as often as to see what ran, and a permanent empty
// "fired last turn" heading trains people to ignore it.
func TestSlashLoreOmitsTheTraceWhenNothingFired(t *testing.T) {
	got := loreBlock(t, &loreCarrier{
		fakeCarrier: newFakeCarrier(),
		entries:     []ctrlproto.LoreEntry{{Name: "the-accord", Keys: []string{"accord"}}},
	})
	if strings.Contains(got, "fired last turn") || strings.Contains(got, "dropped by token budget") {
		t.Errorf("an empty trace should print no heading:\n%s", got)
	}
	if !strings.Contains(got, "the-accord") {
		t.Errorf("the authored entry should still be listed:\n%s", got)
	}
}

// A carrier that does not serve the context surface must not cost the authored
// listing: the two reads are independent, and /lore's first job is the files.
func TestSlashLoreStillListsEntriesWhenTheTraceIsUnavailable(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.surfErr = nil
	i.cfg.Carrier = &traceless{loreCarrier{fakeCarrier: fc,
		entries: []ctrlproto.LoreEntry{{Name: "the-accord", Keys: []string{"accord"}}}}}
	i.slashLore()
	i.mu.Lock()
	got := stripANSICodes(strings.Join(i.helpBlock, "\n"))
	i.mu.Unlock()
	if !strings.Contains(got, "the-accord") {
		t.Errorf("a failing context fetch swallowed the authored entries:\n%s", got)
	}
	if strings.Contains(got, "fired last turn") {
		t.Errorf("no trace was available, so no heading should print:\n%s", got)
	}
}

type traceless struct{ loreCarrier }

func (t *traceless) Context(context.Context, string) (ctrlproto.ContextBreakdown, error) {
	return ctrlproto.ContextBreakdown{}, context.Canceled
}

func stripANSICodes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var _ = tui.Dark
