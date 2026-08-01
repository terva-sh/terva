package modes

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/tui"
)

func archiveView() *ctrlproto.MemoryView {
	return &ctrlproto.MemoryView{
		ProjectBound: true,
		User: ctrlproto.MemoryScope{
			Label: "User memory", Entries: []string{"prefers worktrees"},
			Bytes: 40, MaxBytes: 4096,
			Archived: []ctrlproto.MemoryArchivedEntry{
				{Ref: "user:review-style", Title: "review style", Keys: []string{"review"}},
			},
			ArchivedBytes: 2048,
		},
		Project: ctrlproto.MemoryScope{
			Label: "Project memory", Entries: []string{"uses pnpm"},
			Bytes: 30, MaxBytes: 16384,
			Archived: []ctrlproto.MemoryArchivedEntry{
				{Ref: "project:release", Title: "cutting a release", Keys: []string{"release"},
					Fired: true, MatchedKeys: []string{"release"}},
				{Ref: "project:gate", Title: "the CI gate", Keys: []string{"ci"},
					Fired: true, DroppedForBudget: true},
			},
			ArchivedBytes: 4096,
			Problems:      []string{"broken.md: bad yaml"},
		},
	}
}

// Within a scope the ACTIVE entries come first and the archived follow: the
// order of decreasing presence — what the model is reading now, then what it
// could read if asked. Reading straight down is the whole reason this pane is
// one list rather than two.
func TestArchivedRowsFollowTheirScopesActiveRows(t *testing.T) {
	_, rows, ok := memoryRows(archiveView())
	if !ok {
		t.Fatal("memoryRows refused a populated view")
	}
	var got []string
	for _, r := range rows {
		kind := "active"
		if r.Archived {
			kind = "archived"
		}
		got = append(got, r.Scope+"/"+kind+"/"+r.Text)
	}
	want := []string{
		"user/active/prefers worktrees",
		"user/archived/review style",
		"project/active/uses pnpm",
		"project/archived/cutting a release",
		"project/archived/the CI gate",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("row order =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// An archived row has to carry everything the pane needs to act on it and to
// explain it: the Ref (delete addresses an id, not the text), the keys, and
// last turn's outcome.
func TestArchivedRowsCarryTheirRefKeysAndTrace(t *testing.T) {
	_, rows, _ := memoryRows(archiveView())
	byText := map[string]int{}
	for i, r := range rows {
		byText[r.Text] = i
	}

	rel := rows[byText["cutting a release"]]
	if rel.Ref != "project:release" {
		t.Errorf("archived row Ref = %q; delete would address the wrong thing", rel.Ref)
	}
	if len(rel.Keys) != 1 || rel.Keys[0] != "release" {
		t.Errorf("archived row lost its triggers: %v", rel.Keys)
	}
	if !rel.Fired || rel.Dropped {
		t.Errorf("a fired-and-kept entry traced as fired=%v dropped=%v", rel.Fired, rel.Dropped)
	}

	gate := rows[byText["the CI gate"]]
	if !gate.Fired || !gate.Dropped {
		t.Errorf("a budget-dropped entry traced as fired=%v dropped=%v", gate.Fired, gate.Dropped)
	}
}

// Scope headers report the archive alongside the fill fraction, and carry the
// unreadable-file warning — the only surface that can.
func TestScopeInfoCarriesTheArchiveAndItsProblems(t *testing.T) {
	scopes, _, _ := memoryRows(archiveView())
	for _, s := range scopes {
		if s.Scope != "project" {
			continue
		}
		if s.Count != 1 {
			t.Errorf("the active count absorbed the archived entries: %d", s.Count)
		}
		if s.ArchivedCount != 2 || s.ArchivedBytes != 4096 {
			t.Errorf("archive summary = %d entries / %dB", s.ArchivedCount, s.ArchivedBytes)
		}
		if s.Bytes != 30 {
			t.Errorf("the fill fraction absorbed archived bytes: %d", s.Bytes)
		}
		if len(s.Problems) != 1 {
			t.Errorf("unreadable archive files did not reach the pane: %v", s.Problems)
		}
		return
	}
	t.Fatal("no project scope in the flattened view")
}

// The glance counts archived entries, and counts them SEPARATELY. Reporting
// only the active tier understates what terva is carrying; adding the two
// together hides the one distinction the tier exists to make.
func TestGlanceCountsBothTiersWithoutMergingThem(t *testing.T) {
	i := &Interactive{}
	i.carrierMemory = archiveView()

	got := i.memoryGlance()
	if !strings.Contains(got, "2+3") {
		t.Errorf("glance = %q, want the active and archived counts kept apart", got)
	}

	// No archive: the old shape, unchanged. A bare count must not grow a "+0".
	plain := &ctrlproto.MemoryView{User: ctrlproto.MemoryScope{Entries: []string{"a"}}}
	i2 := &Interactive{}
	i2.carrierMemory = plain
	if got := i2.memoryGlance(); got != "🧠 1" {
		t.Errorf("glance with no archive = %q, want the unchanged bare count", got)
	}

	// Archived-only is still worth showing: the facts exist, they are simply not
	// resident. Reporting nothing would say terva knows nothing.
	only := &ctrlproto.MemoryView{Project: ctrlproto.MemoryScope{
		Archived: []ctrlproto.MemoryArchivedEntry{{Ref: "project:x"}},
	}}
	i3 := &Interactive{}
	i3.carrierMemory = only
	if got := i3.memoryGlance(); !strings.Contains(got, "0+1") {
		t.Errorf("archived-only glance = %q, want it to report what is stored", got)
	}
}

// A forget from the pane must go out as the ARCHIVE's verb carrying the Ref —
// not down the active tier's remove path, which matches entry TEXT and would
// either miss or delete an unrelated active entry sharing those words.
func TestForgetActionSendsTheArchiveVerb(t *testing.T) {
	c := &memActionCarrier{}
	i := newMemPaneInteractive(c)
	i.applyMemoryAction(dialogs.MemoryAction{Forget: true, Scope: "project", Entry: "project:release"})

	if c.action != "forget" {
		t.Errorf("pane sent %q, want forget", c.action)
	}
	if c.args["entry"] != "project:release" || c.args["scope"] != "project" {
		t.Errorf("forget args = %v", c.args)
	}

	// And the active path is untouched: remove still carries the full text.
	c2 := &memActionCarrier{}
	i2 := newMemPaneInteractive(c2)
	i2.applyMemoryAction(dialogs.MemoryAction{Remove: true, Scope: "user", Entry: "prefers worktrees"})
	if c2.action != "remove" || c2.args["entry"] != "prefers worktrees" {
		t.Errorf("active remove = %q %v", c2.action, c2.args)
	}
}

func newMemPaneInteractive(c Carrier) *Interactive {
	return &Interactive{
		turns:        newTurnEngine(),
		dirty:        make(chan struct{}, 1),
		memoryDialog: dialogs.NewMemoryDialog(),
		cfg:          InteractiveConfig{Carrier: c, CarrierSession: "s1", Theme: tui.Dark},
	}
}

// memActionCarrier records the one SurfaceAction the pane issues. The embedded
// interface covers every other verb: calling one panics, which is itself the
// assertion that the pane does not stray off this path.
type memActionCarrier struct {
	ctrlproto.WorkspaceService
	action string
	args   map[string]string
}

func (c *memActionCarrier) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	c.action, c.args = action, args
	return nil
}

// Surface answers the refresh that follows a mutation, so applyMemoryAction can
// complete without the embedded nil interface panicking.
func (c *memActionCarrier) Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error) {
	return ctrlproto.Surface{ID: id, Kind: "memory", Memory: archiveView()}, nil
}

// SubscribeReliable completes the Carrier interface. Never called here; the pane
// issues an action and re-reads a surface, nothing more.
func (c *memActionCarrier) SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	return nil, nil
}
