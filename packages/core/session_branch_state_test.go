package core

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// configuredStageSession builds a session with every mutable field set, so a
// test can ask what a fork keeps without listing the fields it expects to
// survive — the list of what to check is the struct itself.
func configuredStageSession(t *testing.T) (*Session, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "anthropic", "claude-opus-5", "0.126.19")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	seed(t, s)
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("SetCreationSpec", s.SetCreationSpec("director", "play", "card_kobeni", map[string]string{"Ada": "card_ada"}, 2))
	must("SetCast", s.SetCast(
		map[string]string{"Ada": "card_ada"},
		map[string]CastRoute{"Ada": {Provider: "anthropic", Model: "claude-opus-5"}}))
	must("SetNote", s.SetNote("keep it tense"))
	must("SetBackground", s.SetBackground("bg_rain"))
	must("SetUserPersona", s.SetUserPersona("Kai", "a tired detective", "nonbinary", "they/them"))
	must("SetCoordination", s.SetCoordination("focus:Ada"))
	must("SetWorld", s.SetWorld("world_noir"))
	must("SetWorldLore", s.SetWorldLore([]WorldLoreEntry{
		{Name: "The rain", Constant: true, Content: "it never stops"},
		{Name: "The ledger", Keys: []string{"ledger"}, Content: "three names are crossed out"},
	}))
	return s, dir
}

// TestBranchCarriesTheWholeSession is the defect this file exists for: the
// child's meta was a hand-written whitelist of five fields, so forking a
// configured Stage session produced one with no note, no backdrop, no bound
// user, no coordination, no World, no cast-model pins and no lorebook — nine
// of fourteen fields dropped, silently, by a fork that looked like it worked.
func TestBranchCarriesTheWholeSession(t *testing.T) {
	parent, dir := configuredStageSession(t)
	path := parent.Path
	if err := parent.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	childPath, err := BranchSession(path, dir, dir, "0.126.19", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	before, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("open parent: %v", err)
	}
	defer before.Close()
	after, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	defer after.Close()

	for _, c := range []struct {
		field       string
		parent, kid any
	}{
		{"Model", before.Meta.Model, after.Meta.Model},
		{"Provider", before.Meta.Provider, after.Meta.Provider},
		{"Persona", before.Meta.Persona, after.Meta.Persona},
		{"Experience", before.Meta.Experience, after.Meta.Experience},
		{"Card", before.Meta.Card, after.Meta.Card},
		{"Cast", before.Meta.Cast, after.Meta.Cast},
		{"CastModels", before.Meta.CastModels, after.Meta.CastModels},
		{"Greeting", before.Meta.Greeting, after.Meta.Greeting},
		{"Note", before.Meta.Note, after.Meta.Note},
		{"Background", before.Meta.Background, after.Meta.Background},
		{"UserName", before.Meta.UserName, after.Meta.UserName},
		{"UserDescription", before.Meta.UserDescription, after.Meta.UserDescription},
		{"UserGender", before.Meta.UserGender, after.Meta.UserGender},
		{"UserPronouns", before.Meta.UserPronouns, after.Meta.UserPronouns},
		{"Coordination", before.Meta.Coordination, after.Meta.Coordination},
		{"World", before.Meta.World, after.Meta.World},
		{"WorldLore", before.Meta.WorldLore, after.Meta.WorldLore},
	} {
		// A parent-vs-child comparison passes when BOTH sides lost the field, so
		// the parent side has to be shown to hold something first. Without this
		// the whole table goes quietly vacuous the day a setter or the loader
		// breaks — which is how the original defect survived as long as it did.
		if reflect.DeepEqual(c.parent, reflect.Zero(reflect.TypeOf(c.parent)).Interface()) {
			t.Errorf("%s: the PARENT holds no value, so comparing it to the branch proves nothing", c.field)
			continue
		}
		if !reflect.DeepEqual(c.parent, c.kid) {
			t.Errorf("%s: parent %v, branch %v — the fork dropped it", c.field, c.parent, c.kid)
		}
	}
}

// TestBranchKeepsItsOwnIdentity is the other half, and the reason the carry is
// written as a whitelist of what to OVERWRITE: everything travels, so the few
// fields that must NOT have to be named. A branch that inherited its parent's
// id would be a second session claiming to be the first.
func TestBranchKeepsItsOwnIdentity(t *testing.T) {
	parent, dir := configuredStageSession(t)
	path, parentID := parent.Path, parent.Meta.ID
	if err := parent.SetParent("some-earlier-session"); err != nil {
		t.Fatalf("SetParent: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	childPath, err := BranchSession(path, dir, dir, "0.127.0", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	child, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	defer child.Close()

	if child.Meta.ID == parentID || child.Meta.ID == "" {
		t.Errorf("branch id = %q, parent %q — a branch needs its own", child.Meta.ID, parentID)
	}
	if child.Meta.Parent != parentID {
		t.Errorf("branch Parent = %q, want the parent's id %q — the tree picker walks this", child.Meta.Parent, parentID)
	}
	if child.Meta.ForkPoint != 1 {
		t.Errorf("branch ForkPoint = %d, want 1", child.Meta.ForkPoint)
	}
	if child.Meta.Version != "0.127.0" {
		t.Errorf("branch Version = %q, want the build that wrote it", child.Meta.Version)
	}
	if child.Meta.Started.Before(parent.Meta.Started) {
		t.Errorf("branch Started %v predates the parent's %v", child.Meta.Started, parent.Meta.Started)
	}
}

// TestBranchDoesNotInheritTheParentsTitle: a meta-row title reads as
// user-chosen, which is what blocks automatic re-titling. An inherited one
// would leave the fork named after the scene it diverged from, forever.
func TestBranchDoesNotInheritTheParentsTitle(t *testing.T) {
	parent, dir := configuredStageSession(t)
	parent.Meta.Title = "The rain scene"
	if err := parent.writeMeta(); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}
	path := parent.Path
	if err := parent.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	childPath, err := BranchSession(path, dir, dir, "0.126.19", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	child, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	defer child.Close()
	if child.Meta.Title != "" {
		t.Errorf("branch title = %q, want empty so it can be titled for itself", child.Meta.Title)
	}
	if child.TitleGenerated {
		t.Errorf("an untitled branch must not claim a generated title")
	}
}

// TestBranchCarriesALegacyLorebook: the parent may store its book on its meta
// rows (written before format version 4). The branch reads both forms and
// writes one, so a fork of an old Stage session arrives with its lore.
func TestBranchCarriesALegacyLorebook(t *testing.T) {
	legacy := []WorldLoreEntry{{Name: "The rain", Constant: true, Content: "it never stops"}}
	path := writeLegacySession(t, sessionFormatVersionAmend, legacy)
	dir := testsupport.TempDir(t)

	childPath, err := BranchSession(path, dir, dir, "0.126.19", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	child, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	defer child.Close()
	if !reflect.DeepEqual(child.Meta.WorldLore, legacy) {
		t.Errorf("branch of a pre-v4 session has %d lore entries, want %d", len(child.Meta.WorldLore), len(legacy))
	}
	if child.Meta.Experience != "play" {
		t.Errorf("branch Experience = %q, want play", child.Meta.Experience)
	}
}

// TestBranchOfALoreFreeSessionDeclaresNoLoreFormat: a coding session's fork has
// no book, so it must not claim a version that makes an older build warn.
func TestBranchOfALoreFreeSessionDeclaresNoLoreFormat(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "anthropic", "claude-opus-5", "0.126.19")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	seed(t, s)
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	childPath, err := BranchSession(path, dir, dir, "0.126.19", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	child, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	defer child.Close()
	if got := child.Meta.FormatVersion; got != sessionFormatVersion {
		t.Errorf("lore-free branch declares format v%d, want v%d", got, sessionFormatVersion)
	}
	if rows := loreRows(t, childPath); len(rows) != 0 {
		t.Errorf("lore-free branch wrote %d lore rows", len(rows))
	}
}

// TestBranchLoreSurvivesALaterMetaWrite is why the branch declares the lore
// format when it carries a book. Without the bump the child's meta row says v2,
// so the loader reads that row as authoritative for lore — and the first
// background change on the fork appends another v2 row with no world_lore,
// erasing the inherited book.
func TestBranchLoreSurvivesALaterMetaWrite(t *testing.T) {
	parent, dir := configuredStageSession(t)
	want := cloneLore(parent.Meta.WorldLore)
	path := parent.Path
	if err := parent.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	childPath, err := BranchSession(path, dir, dir, "0.126.19", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	child, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	if got := child.Meta.FormatVersion; got != sessionFormatVersionLore {
		t.Errorf("a branch carrying a book declares format v%d, want v%d", got, sessionFormatVersionLore)
	}
	if err := child.SetBackground("bg_fog"); err != nil {
		t.Fatalf("SetBackground: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("close child: %v", err)
	}
	reopened, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("reopen child: %v", err)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(reopened.Meta.WorldLore, want) {
		t.Errorf("after a background change the branch has %d lore entries, want %d — a meta write erased the inherited book",
			len(reopened.Meta.WorldLore), len(want))
	}
}

// TestBranchLoreIsItsOwnCopy: the child inherits the state, not the parent's
// edit history, and editing one must not touch the other.
func TestBranchLoreIsItsOwnCopy(t *testing.T) {
	parent, dir := configuredStageSession(t)
	path := parent.Path
	if err := parent.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	childPath, err := BranchSession(path, dir, dir, "0.126.19", 1)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	child, _, err := OpenSession(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	edited := cloneLore(child.Meta.WorldLore)
	edited[0].Content = "it stopped"
	if err := child.SetWorldLore(edited); err != nil {
		t.Fatalf("SetWorldLore: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("close child: %v", err)
	}
	reopenedParent, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen parent: %v", err)
	}
	defer reopenedParent.Close()
	if got := reopenedParent.Meta.WorldLore[0].Content; got != "it never stops" {
		t.Errorf("editing the branch changed the parent's lore to %q", got)
	}
}
