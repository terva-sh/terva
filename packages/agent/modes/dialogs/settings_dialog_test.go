package dialogs

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/tui"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// cells is the painted width of a rendered line: escapes cost nothing.
func cells(s string) int { return runewidth.StringWidth(ansiRE.ReplaceAllString(s, "")) }

func widestLine(lines []string) (int, string) {
	worst, at := 0, ""
	for _, l := range lines {
		if w := cells(l); w > worst {
			worst, at = w, l
		}
	}
	return worst, at
}

// settingsFixture mixes the shapes that overflow: a long note (the real
// `approval` row already exceeds 100 cells), a long option label, and
// non-ASCII text whose byte length overstates its painted width.
func settingsFixture() []SettingsItem {
	return []SettingsItem{
		{Key: "approval", Label: "Approval mode",
			Options: []SettingsOption{{Value: "ask", Label: "ask — prompt everything"}}, Choice: 0,
			Desc: "How tool calls are gated for this session.",
			Hint: "per-session — not saved (a security posture, like the TUI)"},
		{Key: "reasoning_summary", Label: "Record thinking",
			Options: []SettingsOption{{Value: "concise", Label: "concise — a short précis per turn"}}, Choice: 0,
			Desc: "Persist a readable summary of the model's reasoning, so an unattended run can be reviewed for why it acted.",
			Hint: "openai-codex only — writes reasoning to disk"},
		{Key: "short", Label: "Background sub-agents", Hint: "applies to new sessions"},
	}
}

// No rendered line may paint wider than the dialog it sits in — at any width.
// The frame rule is drawn to exactly `width`, so a row that exceeds it spills
// past the border and wraps in the terminal, shunting the rest of the dialog
// down a line.
func TestSettingsDialogRowsFitWidth(t *testing.T) {
	for _, width := range []int{120, 100, 80, 60, 40} {
		d := NewSettingsDialog()
		d.Open(settingsFixture())
		got, worst := widestLine(d.Render(tui.Dark, width))
		if got > width {
			t.Errorf("width %d: a line painted %d cells: %q", width, got, ansiRE.ReplaceAllString(worst, ""))
		}
	}
}

// The options sub-view has its own overflow path: the item description was
// emitted unwrapped there while the main view wraps the same string.
func TestSettingsOptionsViewFitsWidth(t *testing.T) {
	for _, width := range []int{80, 60, 40} {
		d := NewSettingsDialog()
		d.Open(settingsFixture())
		d.cursor = 1
		d.selecting = true
		got, worst := widestLine(d.Render(tui.Dark, width))
		if got > width {
			t.Errorf("width %d: an options line painted %d cells: %q", width, got, ansiRE.ReplaceAllString(worst, ""))
		}
	}
}

// A hint that cannot ride inline moves to its own line rather than being
// dropped: it carries the caveat ("per-session — not saved") that explains
// what the row does, so losing it silently would be worse than overflowing.
func TestSettingsLongHintMovesToOwnLineAndSurvives(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(settingsFixture())
	out := ansiRE.ReplaceAllString(strings.Join(d.Render(tui.Dark, 60), "\n"), "")
	if !strings.Contains(out, "per-session") {
		t.Errorf("the long hint was dropped entirely:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Approval mode") && strings.Contains(line, "per-session") {
			t.Errorf("hint still inline on an over-wide row: %q", line)
		}
	}
}

// A hint that fits stays inline — the compact form is the common case and
// must not regress into an extra line for every row.
func TestSettingsShortHintStaysInline(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(settingsFixture())
	for _, line := range d.Render(tui.Dark, 100) {
		plain := ansiRE.ReplaceAllString(line, "")
		if strings.Contains(plain, "Background sub-agents") {
			if !strings.Contains(plain, "applies to new sessions") {
				t.Errorf("a short hint left the row: %q", plain)
			}
			return
		}
	}
	t.Fatal("the short-hint row was not rendered")
}

// truncate measures painted cells and cuts on a rune boundary. The byte-based
// version sliced mid-rune, so an em dash or an accented word came out as
// invalid UTF-8 — and it counted a 3-byte "—" as 3 cells, cutting far earlier
// than the terminal required.
func TestTruncateIsRuneAndWidthSafe(t *testing.T) {
	const s = "openai-codex — writes précis to disk"
	for max := 2; max <= runewidth.StringWidth(s)+2; max++ {
		got := truncate(s, max)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(%q, %d) = %q: not valid UTF-8", s, max, got)
		}
		if w := runewidth.StringWidth(got); w > max {
			t.Errorf("truncate(%q, %d) painted %d cells: %q", s, max, w, got)
		}
	}
	if got := truncate(s, 1000); got != s {
		t.Errorf("a string that fits must be returned unchanged, got %q", got)
	}
	// Width, not bytes: this fits in 36 cells but is 40 bytes, and the
	// byte-based version truncated it needlessly.
	if got := truncate(s, 36); got != s {
		t.Errorf("truncate cut a string that fits in 36 cells: %q", got)
	}
}
