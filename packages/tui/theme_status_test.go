package tui

import (
	"os"
	"path/filepath"
	"strings"
	"terva.sh/terva/packages/testsupport"
	"testing"
)

func TestMeterColorStages(t *testing.T) {
	th := Theme{Muted: 1, Warning: 2, Error: 3} // zero slots: classic fallback
	for pct, want := range map[float64]int{0: 1, 69.9: 1, 70: 2, 89.9: 2, 90: 3, 100: 3} {
		if got := th.MeterColor(pct); got != want {
			t.Errorf("MeterColor(%v) = %d, want %d", pct, got, want)
		}
	}
	ramp := Theme{Muted: 1, Warning: 2, Error: 3, MeterLow: 44, MeterMid: 214, MeterHigh: 201}
	for pct, want := range map[float64]int{10: 44, 75: 214, 95: 201} {
		if got := ramp.MeterColor(pct); got != want {
			t.Errorf("ramp MeterColor(%v) = %d, want %d", pct, got, want)
		}
	}
}

func TestStatusColorFallback(t *testing.T) {
	th := Theme{Muted: 7, StatusColors: map[string]int{"cwd": 81}}
	if got := th.StatusColor(SegCWD, th.Muted); got != 81 {
		t.Errorf("StatusColor(cwd) = %d, want the theme override 81", got)
	}
	if got := th.StatusColor(SegModel, th.Muted); got != 7 {
		t.Errorf("StatusColor(model) = %d, want the fallback 7", got)
	}
}

// The meter ramp reaches the rendered bar: a theme with staged colors
// paints a hot context meter with its high stage.
func TestContextMeterUsesThemeRamp(t *testing.T) {
	th := Dark
	th.MeterHigh = 201
	atoms := segContext(StatusBarParams{Theme: th, ContextUsed: 95, ContextMax: 100})
	if !strings.Contains(atoms[0], sgrFG(201)) {
		t.Fatalf("95%% context should use the theme's MeterHigh: %q", atoms[0])
	}
}

// A theme file can set the meter ramp and per-segment colors, and the
// dark-daltonized built-in resolves by name.
func TestThemeLoaderStatusColors(t *testing.T) {
	home := testsupport.TempDir(t)
	dir := filepath.Join(home, "themes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	theme := `{
		"name": "test-status",
		"meter_low": 44, "meter_mid": 214, "meter_high": 201,
		"status_colors": {"cwd": 81, "git": 179}
	}`
	if err := os.WriteFile(filepath.Join(dir, "test-status.json"), []byte(theme), 0o644); err != nil {
		t.Fatal(err)
	}
	th, name, err := LoadThemeFromHome(home, "test-status", Dark)
	if err != nil || name != "test-status" {
		t.Fatalf("load: name=%q err=%v", name, err)
	}
	if th.MeterLow != 44 || th.MeterMid != 214 || th.MeterHigh != 201 {
		t.Errorf("meter ramp not applied: %d/%d/%d", th.MeterLow, th.MeterMid, th.MeterHigh)
	}
	if th.StatusColors["cwd"] != 81 || th.StatusColors["git"] != 179 {
		t.Errorf("status_colors not applied: %v", th.StatusColors)
	}
	// Untouched slots inherit the base theme.
	if th.Accent != Dark.Accent {
		t.Errorf("accent should inherit from base, got %d", th.Accent)
	}
}

func TestDarkDaltonizedBuiltin(t *testing.T) {
	th, name, err := LoadThemeFromHome(testsupport.TempDir(t), "dark-daltonized", Dark)
	if err != nil || name != "dark-daltonized" {
		t.Fatalf("load: name=%q err=%v", name, err)
	}
	if th.Tool == Dark.Tool || th.Error == Dark.Error {
		t.Errorf("daltonized theme must re-base the red/green semantic slots: tool=%d error=%d", th.Tool, th.Error)
	}
	if th.MeterLow == Dark.MeterLow || th.MeterHigh == Dark.MeterHigh {
		t.Errorf("daltonized theme should carry its own meter ramp: %d/%d/%d", th.MeterLow, th.MeterMid, th.MeterHigh)
	}
	// And it shows up in the picker as a builtin.
	found := false
	for _, opt := range AvailableThemes(testsupport.TempDir(t)) {
		if opt.Value == "dark-daltonized" && opt.Builtin {
			found = true
		}
	}
	if !found {
		t.Error("dark-daltonized missing from AvailableThemes")
	}
}

func TestLightDaltonizedBuiltin(t *testing.T) {
	th, name, err := LoadThemeFromHome(testsupport.TempDir(t), "light-daltonized", Light)
	if err != nil || name != "light-daltonized" {
		t.Fatalf("load: name=%q err=%v", name, err)
	}
	if th.Tool == Light.Tool || th.Error == Light.Error {
		t.Errorf("light daltonized must re-base the red/green slots: tool=%d error=%d", th.Tool, th.Error)
	}
	if th.Warning == th.Error {
		t.Errorf("warning and error must stay distinct: both %d", th.Error)
	}
}

// The bare "daltonized" name follows the detected terminal background,
// like "auto" does for the regular defaults.
func TestDaltonizedFollowsDetectedBackground(t *testing.T) {
	home := testsupport.TempDir(t)
	th, name, err := LoadThemeFromHome(home, "daltonized", Dark)
	if err != nil || name != "dark-daltonized" || th.Tool != DarkDaltonized.Tool {
		t.Fatalf("dark terminal: got %q err=%v", name, err)
	}
	th, name, err = LoadThemeFromHome(home, "daltonized", Light)
	if err != nil || name != "light-daltonized" || th.Tool != LightDaltonized.Tool {
		t.Fatalf("light terminal: got %q err=%v", name, err)
	}
}
