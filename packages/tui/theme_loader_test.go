package tui

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestLoadThemeAllowsPartialColorOverrides(t *testing.T) {
	home := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "themes", "partial.json")
	if err := os.WriteFile(path, []byte(`{"colors":{"dark":{"accent":204}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	th, name, err := LoadThemeFromHome(home, "partial", Dark)
	if err != nil {
		t.Fatal(err)
	}
	if name != "partial" {
		t.Fatalf("name = %q, want partial", name)
	}
	if th.Accent != 204 {
		t.Fatalf("accent = %d, want 204", th.Accent)
	}
	if th.FG != Dark.FG {
		t.Fatalf("fg = %d, want inherited %d", th.FG, Dark.FG)
	}
	if len(th.SpinnerFrames) == 0 || len(th.SpinnerMessages) == 0 {
		t.Fatal("spinner defaults should be inherited")
	}
}

func TestLoadThemeAllowsSpinnerOnlyTopLevelOverrides(t *testing.T) {
	home := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "themes", "spinner.json")
	if err := os.WriteFile(path, []byte(`{"spinner_frames":[".","o"],"spinner_messages":["working"],"spinner_interval_ms":200}`), 0o644); err != nil {
		t.Fatal(err)
	}

	th, _, err := LoadThemeFromHome(home, "spinner", Dark)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(th.SpinnerFrames); got != 2 {
		t.Fatalf("spinner frame count = %d, want 2", got)
	}
	if th.SpinnerFrames[1] != "o" || th.SpinnerMessages[0] != "working" || th.SpinnerIntervalMS != 200 {
		t.Fatalf("spinner overrides not applied: %#v %#v %d", th.SpinnerFrames, th.SpinnerMessages, th.SpinnerIntervalMS)
	}
	if th.Accent != Dark.Accent {
		t.Fatalf("accent = %d, want inherited %d", th.Accent, Dark.Accent)
	}
}

func TestLoadThemeFallsBackToDarkWhenLightModeMissing(t *testing.T) {
	home := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "themes", "darkonly.json")
	if err := os.WriteFile(path, []byte(`{"colors":{"dark":{"spinner_frames":["◢","◣","◤","◥"],"spinner_messages":["working"],"spinner_interval_ms":120}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	th, _, err := LoadThemeFromHome(home, "darkonly", Light)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.SpinnerFrames) != 4 || th.SpinnerFrames[0] != "◢" {
		t.Fatalf("spinner frames = %#v, want dark fallback frames", th.SpinnerFrames)
	}
	if len(th.SpinnerMessages) != 1 || th.SpinnerMessages[0] != "working" {
		t.Fatalf("spinner messages = %#v, want dark fallback message", th.SpinnerMessages)
	}
	if th.SpinnerIntervalMS != 120 {
		t.Fatalf("spinner interval = %d, want 120", th.SpinnerIntervalMS)
	}
	if th.FG != Light.FG {
		t.Fatalf("fg = %d, want inherited light fg %d", th.FG, Light.FG)
	}
}

func TestLoadThemeAllowsSharedColorsOverrides(t *testing.T) {
	home := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "themes", "shared.json")
	if err := os.WriteFile(path, []byte(`{"colors":{"accent":204,"spinner_messages":["ship"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	th, _, err := LoadThemeFromHome(home, "shared", Light)
	if err != nil {
		t.Fatal(err)
	}
	if th.Accent != 204 {
		t.Fatalf("accent = %d, want 204", th.Accent)
	}
	if len(th.SpinnerMessages) != 1 || th.SpinnerMessages[0] != "ship" {
		t.Fatalf("spinner messages = %#v, want ship", th.SpinnerMessages)
	}
	if th.FG != Light.FG {
		t.Fatalf("fg = %d, want inherited %d", th.FG, Light.FG)
	}
}
