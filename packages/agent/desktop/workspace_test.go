package desktop

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func none(string) bool                { return false }
func noPick() string                  { return "" }
func panicPick() string               { panic("the picker opened") }
func pickOf(dir string) func() string { return func() string { return dir } }

func TestAnExplicitCWDWins(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	start := testsupport.TempDir(t)
	if err := RememberWorkspace(path, testsupport.TempDir(t)); err != nil {
		t.Fatal(err)
	}
	if dir, src := ChooseWorkspace(path, start, true, none, panicPick); dir != start || src != SourceExplicit {
		t.Fatalf("explicit = (%s, %s)", dir, src)
	}
}

// A start directory that holds a team, or is a checkout, serves without the
// picker: the person started the window where they meant to.
func TestAStartDirectoryWithTeamsOrACheckoutServes(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	teams := testsupport.TempDir(t)
	holds := func(d string) bool { return SameDir(d, teams) }
	if dir, src := ChooseWorkspace(path, teams, false, holds, panicPick); dir != teams || src != SourceStart {
		t.Fatalf("teams = (%s, %s)", dir, src)
	}
	checkout := testsupport.TempDir(t)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if dir, src := ChooseWorkspace(path, checkout, false, none, panicPick); dir != checkout || src != SourceStart {
		t.Fatalf("checkout = (%s, %s)", dir, src)
	}
}

// A launcher starts in $HOME: the window reopens the directory the last
// launch used, and does not ask again.
func TestALauncherReopensTheRememberedDirectory(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	home := testsupport.TempDir(t)
	team := testsupport.TempDir(t)
	if err := RememberWorkspace(path, team); err != nil {
		t.Fatal(err)
	}
	if dir, src := ChooseWorkspace(path, home, false, none, panicPick); dir != team || src != SourceRemembered {
		t.Fatalf("remembered = (%s, %s)", dir, src)
	}
}

// With nothing remembered, the picker asks. A directory that has gone away
// does not count as remembered.
func TestThePickerAsksWhenNothingAnswers(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	home := testsupport.TempDir(t)
	if err := RememberWorkspace(path, filepath.Join(home, "gone")); err != nil {
		t.Fatal(err)
	}
	chosen := testsupport.TempDir(t)
	if dir, src := ChooseWorkspace(path, home, false, none, pickOf(chosen)); dir != chosen || src != SourcePicked {
		t.Fatalf("picked = (%s, %s)", dir, src)
	}
	if dir, src := ChooseWorkspace(path, home, false, none, noPick); dir != home || src != SourceFallback {
		t.Fatalf("cancelled = (%s, %s)", dir, src)
	}
}

// The workspace and the port share one file, and neither write drops the
// other.
func TestPortAndWorkspaceShareTheStateFile(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	team := testsupport.TempDir(t)
	if err := RememberPort(path, 41234); err != nil {
		t.Fatal(err)
	}
	if err := RememberWorkspace(path, team); err != nil {
		t.Fatal(err)
	}
	if err := RememberPort(path, 41235); err != nil {
		t.Fatal(err)
	}
	s := readState(path)
	if s.Port != 41235 || s.Workspace != team {
		t.Fatalf("state = %+v", s)
	}
}
