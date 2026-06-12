package envcompat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// pinStateBase points osDefaultDir at a temp location and returns the
// directory the "terva"/"zot" dirs resolve under. XDG_STATE_HOME covers
// linux; darwin resolves through $HOME/Library/Application Support
// (os.UserHomeDir honors $HOME) and windows through %LOCALAPPDATA%, so
// those get pinned too — without them, these tests read the
// developer's (or CI runner's) real data dirs.
func pinStateBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	switch runtime.GOOS {
	case "darwin":
		home := t.TempDir()
		t.Setenv("HOME", home)
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		t.Setenv("LOCALAPPDATA", base)
	}
	return base
}

func TestLookupPrefersNewSpelling(t *testing.T) {
	t.Setenv("TERVA_PROBE", "new")
	t.Setenv("ZOT_PROBE", "old")
	if v := Get("PROBE"); v != "new" {
		t.Errorf("Get = %q, want the TERVA_ spelling to win", v)
	}

	os.Unsetenv("TERVA_PROBE")
	if v := Get("PROBE"); v != "old" {
		t.Errorf("Get = %q, want the ZOT_ fallback", v)
	}

	os.Unsetenv("ZOT_PROBE")
	if v, ok := Lookup("PROBE"); ok || v != "" {
		t.Errorf("Lookup = %q,%v, want unset", v, ok)
	}

	// Empty-but-set new spelling still wins (matches os.LookupEnv
	// semantics; Get callers treat "" as unset like they always did).
	t.Setenv("TERVA_PROBE", "")
	t.Setenv("ZOT_PROBE", "old")
	if v, ok := Lookup("PROBE"); !ok || v != "" {
		t.Errorf("Lookup = %q,%v, want set-but-empty new spelling", v, ok)
	}
}

func TestHomeResolution(t *testing.T) {
	// Explicit env vars always win, new spelling first.
	t.Setenv("TERVA_HOME", "/x/terva")
	t.Setenv("ZOT_HOME", "/x/zot")
	if got := Home(); got != "/x/terva" {
		t.Errorf("Home = %q, want TERVA_HOME", got)
	}
	os.Unsetenv("TERVA_HOME")
	if got := Home(); got != "/x/zot" {
		t.Errorf("Home = %q, want ZOT_HOME fallback", got)
	}
	os.Unsetenv("ZOT_HOME")

	// OS-default resolution, pinned so the test controls the
	// filesystem. Since the phase-2 rename cut: an existing terva dir
	// wins; an existing zot dir (pre-rename install) keeps winning
	// over a missing terva dir; with neither, fresh installs create
	// the terva dir.
	base := pinStateBase(t)
	if got, want := Home(), filepath.Join(base, "terva"); got != want {
		t.Errorf("Home = %q, want %q (fresh install defaults to the new name)", got, want)
	}
	if err := os.MkdirAll(filepath.Join(base, "zot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := Home(), filepath.Join(base, "zot"); got != want {
		t.Errorf("Home = %q, want %q (pre-rename install keeps its data)", got, want)
	}
	if err := os.MkdirAll(filepath.Join(base, "terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := Home(), filepath.Join(base, "terva"); got != want {
		t.Errorf("Home = %q, want %q (terva dir wins when both exist)", got, want)
	}
}

func TestDeprecationWarningIsOn(t *testing.T) {
	// Dormant through phase 1; ON since the phase-2 rename cut, which
	// started the stated ≥2-release window for ZOT_* spellings.
	warnMu.Lock()
	defer warnMu.Unlock()
	if !warnDeprecated {
		t.Error("the phase-2 cut turns ZOT_* deprecation warnings on")
	}
}

func TestZotFallbackDisabled(t *testing.T) {
	base := pinStateBase(t)
	os.Unsetenv("TERVA_HOME")
	os.Unsetenv("ZOT_HOME")

	// Pre-migration: only the zot dir exists, so discovery lands there.
	if err := os.MkdirAll(filepath.Join(base, "zot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := Home(), filepath.Join(base, "zot"); got != want {
		t.Fatalf("Home = %q, want %q before the flag", got, want)
	}

	if err := SetZotFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	if !ZotFallbackDisabled() {
		t.Fatal("flag not visible after SetZotFallbackDisabled(true)")
	}
	if got, want := Home(), filepath.Join(base, "terva"); got != want {
		t.Errorf("Home = %q, want %q (zot fallback off, even though the terva dir holds only the marker)", got, want)
	}
	if got := ProjectDirNames(); len(got) != 1 || got[0] != ".terva" {
		t.Errorf("ProjectDirNames = %v, want [.terva]", got)
	}
	if _, ok := HomeMigrationNote(); ok {
		t.Error("no migration note once the flag is set")
	}

	// ZOT_* env vars stay honored: explicit user action, not autoloading.
	t.Setenv("ZOT_HOME", filepath.Join(base, "elsewhere"))
	if got, want := Home(), filepath.Join(base, "elsewhere"); got != want {
		t.Errorf("Home = %q, want %q (ZOT_HOME must survive the flag)", got, want)
	}
	os.Unsetenv("ZOT_HOME")

	// Clearing removes the marker; clearing twice is a no-op.
	if err := SetZotFallbackDisabled(false); err != nil {
		t.Fatal(err)
	}
	if ZotFallbackDisabled() {
		t.Error("flag still set after SetZotFallbackDisabled(false)")
	}
	if err := SetZotFallbackDisabled(false); err != nil {
		t.Errorf("double-clear must be a no-op, got %v", err)
	}
	// The terva dir created to hold the marker still exists and wins
	// at step 2; deleting it shows the zot fallback is back in play.
	if err := os.Remove(filepath.Join(base, "terva")); err != nil {
		t.Fatal(err)
	}
	if got, want := Home(), filepath.Join(base, "zot"); got != want {
		t.Errorf("Home = %q, want %q (fallback restored after clear)", got, want)
	}
}

func TestZotFallbackMarkerHonorsTervaHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERVA_HOME", dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := SetZotFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".zot-fallback-disabled")); err != nil {
		t.Errorf("marker not under $TERVA_HOME: %v", err)
	}
	if !ZotFallbackDisabled() {
		t.Error("flag not visible via $TERVA_HOME marker base")
	}
	if err := SetZotFallbackDisabled(false); err != nil {
		t.Fatal(err)
	}
	if ZotFallbackDisabled() {
		t.Error("flag still set after clear")
	}
}

func TestHomeMigrationNoteIsOneShot(t *testing.T) {
	base := pinStateBase(t)

	// Legacy zot dir in use, no terva dir, no env override.
	if err := os.MkdirAll(filepath.Join(base, "zot"), 0o755); err != nil {
		t.Fatal(err)
	}
	note, ok := HomeMigrationNote()
	if !ok || note == "" {
		t.Fatalf("expected a migration note, got ok=%v", ok)
	}
	if _, ok := HomeMigrationNote(); ok {
		t.Error("the note must be one-shot")
	}

	// An explicit env var means the user already decided: no note.
	t.Setenv("ZOT_HOME", filepath.Join(base, "zot"))
	if _, ok := HomeMigrationNote(); ok {
		t.Error("no note when the dir came from an env var")
	}
}
