package tui

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// TestStatusBarTwoRowDefault verifies the default layout: identity +
// spend on row 1 (abbreviated cwd, model, tokens, cost), meters on
// row 2, on a wide terminal.
func TestStatusBarTwoRowDefault(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:    Dark,
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		CWD:      "/tmp/deep/nested/x",
		Usage: provider.Usage{
			InputTokens:  476_000,
			OutputTokens: 3_400,
			CostUSD:      1.242,
		},
		Subscription: true,
		ContextUsed:  55_000,
		ContextMax:   1_000_000,
		Cols:         500, // very wide
	})
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), lines)
	}
	row1 := stripANSI(lines[0])
	if !strings.Contains(row1, "(anthropic) claude-opus-4-7") {
		t.Errorf("row 1 should contain model, got %q", row1)
	}
	if !strings.Contains(row1, "/t/d/n/x") {
		t.Errorf("row 1 should contain the abbreviated cwd, got %q", row1)
	}
	if !strings.Contains(row1, "↑476k ↓3.4k") || !strings.Contains(row1, "$1.242 (sub)") {
		t.Errorf("row 1 should contain tokens and cost, got %q", row1)
	}
	if !strings.Contains(row1, " · ") {
		t.Errorf("row 1 should join segments with the dot separator, got %q", row1)
	}
	row2 := stripANSI(lines[1])
	if !strings.HasPrefix(row2, "  ") {
		t.Errorf("row 2 should start with the 2-space pad, got %q", row2)
	}
	if !strings.Contains(row2, "ctx 55k/1.0M") || !strings.Contains(row2, "6%") {
		t.Errorf("row 2 should carry the context meter, got %q", row2)
	}
}

// TestStatusBarMinimalSession: no cwd, no usage data — a single line
// with just the model segment.
func TestStatusBarMinimalSession(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:    Dark,
		Provider: "openai",
		Model:    "gpt-5.4",
		CWD:      "",
		Cols:     200,
	})
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(stripANSI(lines[0]), "(openai) gpt-5.4") {
		t.Errorf("line should contain the model, got %q", lines[0])
	}
}

// TestStatusBarReplaySegment: the session-player scrubber leads row 1 when set
// and vanishes otherwise.
func TestStatusBarReplaySegment(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:  Dark,
		Model:  "gpt-5.5",
		Replay: "▶ 38%  2×",
		Cols:   200,
	})
	if got := stripANSI(strings.Join(lines, "\n")); !strings.Contains(got, "▶ 38%  2×") {
		t.Errorf("status bar should carry the replay scrubber, got %q", got)
	}
	bare := StatusBar(StatusBarParams{Theme: Dark, Model: "gpt-5.5", Cols: 200})
	if strings.Contains(stripANSI(strings.Join(bare, "\n")), "▶") {
		t.Error("replay segment must vanish when Replay is empty")
	}
}

func TestStatusBarThinkingLevelBetweenModelAndStats(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:     Dark,
		Provider:  "openai-codex",
		Model:     "gpt-5.5",
		Reasoning: "minimum",
		CWD:       "/tmp/x",
		Usage: provider.Usage{
			InputTokens:  4_300_000,
			OutputTokens: 2,
		},
		Cols: 500,
	})
	plain := stripANSI(lines[0])
	modelIdx := strings.Index(plain, "(openai-codex) gpt-5.5")
	thinkingIdx := strings.Index(plain, "thinking: minimal")
	statsIdx := strings.Index(plain, "↑4.3M")
	if modelIdx < 0 || thinkingIdx < 0 || statsIdx < 0 {
		t.Fatalf("row 1 should contain model, thinking level, and tokens, got %q", plain)
	}
	if !(modelIdx < thinkingIdx && thinkingIdx < statsIdx) {
		t.Fatalf("thinking level should sit between model and tokens, got %q", plain)
	}
}

// Extension status segments appear as ambient atoms on the meters row.
func TestThinkingLevelLabel(t *testing.T) {
	// "max" is a distinct tier above "maximum" and must show as its own
	// label, not collapse into "maximum" (which would hide the selection).
	cases := map[string]string{
		"":        "",
		"off":     "",
		"minimum": "minimal",
		"min":     "minimal",
		"low":     "low",
		"medium":  "medium",
		"high":    "high",
		"maximum": "maximum",
		"xhigh":   "maximum",
		"max":     "max",
	}
	for in, want := range cases {
		if got := thinkingLevelLabel(in); got != want {
			t.Errorf("thinkingLevelLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusBarExtStatusSegments(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:     Dark,
		Provider:  "anthropic",
		Model:     "claude-opus-4-7",
		CWD:       "/tmp/x",
		ExtStatus: []string{"▸ patch parser (1/4)", ""}, // empty is skipped
		Cols:      500,
	})
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "patch parser (1/4)") {
		t.Errorf("ext status segment missing from status bar:\n%s", joined)
	}
}

func TestStatusBarTaskGlanceSegment(t *testing.T) {
	base := StatusBarParams{
		Theme:    Dark,
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		CWD:      "/tmp/x",
		Cols:     500,
	}
	// Present when there's an active task…
	base.TaskGlance = "▸ Wiring the panel (2/5)"
	joined := stripANSI(strings.Join(StatusBar(base), "\n"))
	if !strings.Contains(joined, "Wiring the panel (2/5)") {
		t.Errorf("task glance missing from status bar:\n%s", joined)
	}
	// …and vanishes (with its separators) when empty.
	base.TaskGlance = ""
	joined = stripANSI(strings.Join(StatusBar(base), "\n"))
	if strings.Contains(joined, "Wiring the panel") {
		t.Errorf("task glance should be absent when empty:\n%s", joined)
	}
}

// The classifier badge answers "is a model deciding my approvals right now?",
// which a user must be able to read at a glance or the opt-in is meaningless.
func TestStatusBarClassifierTag(t *testing.T) {
	bar := func(classifier string) []string {
		return StatusBar(StatusBarParams{
			Theme: Dark, Provider: "openai", Model: "gpt-5.5", CWD: "/tmp/x",
			ApprovalMode: "workspace", ClassifierMode: classifier, Cols: 200,
		})
	}
	text := func(classifier string) string { return stripANSI(strings.Join(bar(classifier), "\n")) }

	// Off, and the zero value, must render nothing at all: the default posture
	// needs no badge, and a badge that is always there stops being read.
	for _, quiet := range []string{"", "off"} {
		if got := text(quiet); strings.Contains(got, "screened") || strings.Contains(got, "approving") {
			t.Errorf("classifier %q rendered a badge: %q", quiet, got)
		}
	}

	// It MODIFIES the approval mode rather than replacing it, so both show.
	screened := text("screen")
	if !strings.Contains(screened, "screened") {
		t.Errorf("screen badge missing: %q", screened)
	}
	if !strings.Contains(screened, "workspace mode") {
		t.Errorf("classifier badge replaced the approval mode instead of modifying it: %q", screened)
	}

	// Approve is the posture where a tool call can run with nobody asked, so
	// it takes the same warning colour yolo does. This is the assertion that
	// keeps the riskiest state from being the quiet one.
	approving := bar("approve")
	if got := stripANSI(strings.Join(approving, "\n")); !strings.Contains(got, "approving") {
		t.Fatalf("approve badge missing: %q", got)
	}
	warn := Dark.FG256(Dark.Warning, "⚖! approving")
	if !strings.Contains(strings.Join(approving, "\n"), warn) {
		t.Error("approve must render in the warning color — a model answering for you cannot be the quiet posture")
	}

	// They must also read apart with no colour at all: a screenshot, a log, a
	// monochrome terminal. Same glyph plus different words is not enough on
	// its own, which is why approve carries the bang.
	if text("screen") == text("approve") {
		t.Fatal("screen and approve render identically once colour is stripped")
	}
}

// The approval posture is the one segment a user must be able to trust at a
// glance, so it gets asserted rather than assumed.
//
// This replaces TestStatusBarNoYoloTag, which drove the same segment through a
// NoYolo bool. That field was superseded by ApprovalMode and its last writer
// was InteractiveConfig.NoYolo — nil under every frontend once the direct
// driver went away — so the tag it rendered could not appear. The successor had
// no test of its own; this is it.
func TestStatusBarApprovalModeTag(t *testing.T) {
	tag := func(mode string) string {
		return stripANSI(strings.Join(StatusBar(StatusBarParams{
			Theme:        Dark,
			Provider:     "openai",
			Model:        "gpt-5.5",
			CWD:          "/tmp/x",
			ApprovalMode: mode,
			Cols:         200,
		}), "\n"))
	}

	if got := tag("workspace"); !strings.Contains(got, "workspace mode") {
		t.Errorf("approval-mode tag missing: %q", got)
	}

	// Yolo runs every tool without asking, so it must render — and in the
	// warning color, which is the whole reason it is a separate arm.
	yolo := StatusBar(StatusBarParams{
		Theme: Dark, Provider: "openai", Model: "gpt-5.5", CWD: "/tmp/x",
		ApprovalMode: "yolo", Cols: 200,
	})
	if got := stripANSI(strings.Join(yolo, "\n")); !strings.Contains(got, "yolo mode") {
		t.Fatalf("yolo tag missing: %q", got)
	}
	warn := Dark.FG256(Dark.Warning, "yolo mode")
	if !strings.Contains(strings.Join(yolo, "\n"), warn) {
		t.Error("yolo must render in the warning color — the riskiest posture cannot be the quiet one")
	}

	// Empty means the carrier has not reported one yet; showing a mode we do
	// not know would be worse than showing none.
	if got := tag(""); strings.Contains(got, "mode") {
		t.Errorf("no tag should render before the mode is known: %q", got)
	}
}

// Greedy wrap: every emitted line fits Cols, atoms are never split,
// and continuation lines carry the 2-space pad.
func TestStatusBarGreedyWrapNarrow(t *testing.T) {
	p := StatusBarParams{
		Theme:     Dark,
		Provider:  "openai-codex",
		Model:     "gpt-5.5",
		Reasoning: "minimum",
		CWD:       "/tmp/x",
		Usage: provider.Usage{
			InputTokens:  4_300_000,
			OutputTokens: 2,
			CostUSD:      0.5,
		},
		ContextUsed: 100_000,
		ContextMax:  272_000,
		Cols:        40,
	}
	lines := StatusBar(p)
	if len(lines) < 3 {
		t.Fatalf("narrow terminal should wrap into several lines, got %d: %q", len(lines), lines)
	}
	for i, line := range lines {
		if w := visibleWidth(line); w > p.Cols {
			t.Errorf("line %d spans %d cells > %d: %q", i, w, p.Cols, stripANSI(line))
		}
		if !strings.HasPrefix(stripANSI(line), "  ") {
			t.Errorf("line %d missing the 2-space pad: %q", i, stripANSI(line))
		}
	}
	joined := stripANSI(strings.Join(lines, "\n"))
	for _, atom := range []string{"(openai-codex) gpt-5.5", "thinking: minimal", "↑4.3M ↓2", "$0.500", "/t/x"} {
		if !strings.Contains(joined, atom) {
			t.Errorf("atom %q lost or split in narrow wrap:\n%s", atom, joined)
		}
	}
}

// The busy spinner prefix glues to the first line when it fits, and
// takes its own line when the terminal is too narrow.
func TestStatusBarBusyPrefixGlue(t *testing.T) {
	base := StatusBarParams{
		Theme:      Dark,
		Provider:   "openai",
		Model:      "gpt-5.5",
		BusyPrefix: "* telling excuses - 1m30s",
		Cols:       120,
	}
	lines := StatusBar(base)
	plain := stripANSI(lines[0])
	if !strings.Contains(plain, "telling excuses") || !strings.Contains(plain, "(openai) gpt-5.5") {
		t.Fatalf("wide: busy prefix should share line 1 with the model, got %q", plain)
	}

	narrow := base
	narrow.Cols = 30
	lines = StatusBar(narrow)
	if len(lines) < 2 {
		t.Fatalf("narrow: want busy prefix on its own line, got %q", lines)
	}
	if !strings.Contains(stripANSI(lines[0]), "telling excuses") {
		t.Errorf("narrow: line 1 should be the busy prefix, got %q", stripANSI(lines[0]))
	}
	if strings.Contains(stripANSI(lines[0]), "gpt-5.5") {
		t.Errorf("narrow: model should move off the busy line, got %q", stripANSI(lines[0]))
	}
}

// Rows config overrides the default layout; unknown IDs are skipped.
func TestStatusBarRowsOverride(t *testing.T) {
	p := StatusBarParams{
		Theme:       Dark,
		Provider:    "openai",
		Model:       "gpt-5.5",
		CWD:         "/tmp/x",
		ContextUsed: 100_000,
		ContextMax:  272_000,
		Rows:        [][]string{{"model", "context", "definitely-not-a-segment"}},
		Cols:        500,
	}
	lines := StatusBar(p)
	if len(lines) != 1 {
		t.Fatalf("configured single row should emit one line, got %d: %q", len(lines), lines)
	}
	plain := stripANSI(lines[0])
	if !strings.Contains(plain, "(openai) gpt-5.5") || !strings.Contains(plain, "ctx 100k/272k") {
		t.Errorf("configured segments missing: %q", plain)
	}
	if strings.Contains(plain, "/t/x") {
		t.Errorf("cwd should not render when omitted from Rows: %q", plain)
	}

	// A config of only unknown IDs falls back to the defaults.
	p.Rows = [][]string{{"bogus"}}
	lines = StatusBar(p)
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), "/t/x") {
		t.Errorf("unknown-only Rows should fall back to defaults:\n%q", lines)
	}
}

// The immersive preset (HideWorkspace) drops cwd/git/tags even when an
// explicit Rows config names them.
func TestStatusBarHideWorkspaceGatesExplicitRows(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:         Dark,
		Provider:      "anthropic",
		Model:         "m",
		CWD:           "/tmp/secret",
		Locked:        true,
		Git:           GitInfo{Present: true, Branch: "main"},
		HideWorkspace: true,
		Rows:          [][]string{{"cwd", "git", "tags", "model"}},
		Cols:          500,
	})
	joined := stripANSI(strings.Join(lines, "\n"))
	if strings.Contains(joined, "secret") || strings.Contains(joined, "main") || strings.Contains(joined, "jailed") {
		t.Fatalf("workspace segments must stay hidden in immersive mode: %q", joined)
	}
	if !strings.Contains(joined, "(anthropic) m") {
		t.Fatalf("model should still render: %q", joined)
	}
}

// ---- segment format tests ----

func TestAbbreviatePath(t *testing.T) {
	for in, want := range map[string]string{
		"~/Workspace/forge.example.com/terva-sh/terva": "~/W/f/t/terva",
		"/tmp/x":            "/t/x",
		"/tmp/deep/nest/x":  "/t/d/n/x",
		"~/x":               "~/x",
		"~":                 "~",
		"":                  "",
		"~/.config/foo/bar": "~/.c/f/bar",
	} {
		if got := abbreviatePath(in); got != want {
			t.Errorf("abbreviatePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitSegment(t *testing.T) {
	th := Dark
	cases := []struct {
		name string
		git  GitInfo
		want string // stripped text; "" = absent
	}{
		{"absent", GitInfo{}, ""},
		{"clean", GitInfo{Present: true, Branch: "main"}, "⎇ main"},
		{"dirty with stats", GitInfo{Present: true, Branch: "sothr-main", Dirty: true, Added: 499, Removed: 109}, "⎇ sothr-main* +499 -109"},
		{"added only", GitInfo{Present: true, Branch: "b", Added: 3}, "⎇ b +3"},
		{"detached", GitInfo{Present: true, Branch: "a1b2c3d", Dirty: false}, "⎇ a1b2c3d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			atoms := segGit(StatusBarParams{Theme: th, Git: tc.git})
			if tc.want == "" {
				if len(atoms) != 0 {
					t.Fatalf("want absent, got %q", atoms)
				}
				return
			}
			if len(atoms) != 1 || stripANSI(atoms[0]) != tc.want {
				t.Fatalf("got %q, want %q", atoms, tc.want)
			}
		})
	}
}

func TestContextSegment(t *testing.T) {
	th := Dark
	// 74% — warning color, meter 4/5 filled.
	atoms := segContext(StatusBarParams{Theme: th, ContextUsed: 201_280, ContextMax: 272_000})
	if len(atoms) != 1 {
		t.Fatalf("want one atom, got %q", atoms)
	}
	plain := stripANSI(atoms[0])
	if plain != "ctx 201k/272k ▓▓▓▓░ 74%" {
		t.Errorf("context atom = %q", plain)
	}
	if !strings.Contains(atoms[0], sgrFG(th.Warning)) {
		t.Errorf("74%% should use the warning color: %q", atoms[0])
	}

	// 91% — error color.
	atoms = segContext(StatusBarParams{Theme: th, ContextUsed: 91, ContextMax: 100})
	if !strings.Contains(atoms[0], sgrFG(th.Error)) {
		t.Errorf("91%% should use the error color: %q", atoms[0])
	}

	// Auto-compacting suffix.
	atoms = segContext(StatusBarParams{Theme: th, ContextUsed: 50, ContextMax: 100, AutoCompacting: true})
	if !strings.Contains(stripANSI(atoms[0]), "(auto)") {
		t.Errorf("auto-compacting suffix missing: %q", atoms[0])
	}

	// No max: raw count fallback.
	atoms = segContext(StatusBarParams{Theme: th, ContextUsed: 4_200})
	if got := stripANSI(atoms[0]); got != "ctx 4.2k" {
		t.Errorf("no-max fallback = %q", got)
	}

	// No data at all: absent.
	if atoms = segContext(StatusBarParams{Theme: th}); len(atoms) != 0 {
		t.Errorf("want absent with no data, got %q", atoms)
	}
}

func TestUsageSegment(t *testing.T) {
	th := Dark
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	p := StatusBarParams{
		Theme: th,
		Now:   now,
		UsageWindows: []provider.UsageWindow{
			{Label: "5h", UsedPercent: 15, ResetsAt: now.Add(4*time.Hour + 33*time.Minute + 20*time.Second)},
			{Label: "weekly", UsedPercent: 8, ResetsAt: now.Add(3*24*time.Hour + 17*time.Hour)},
			{Label: "credits", UsedPercent: -1},
		},
	}
	atoms := segUsage(p)
	if len(atoms) != 3 {
		t.Fatalf("want one atom per window, got %q", atoms)
	}
	if got := stripANSI(atoms[0]); got != "5h ▓░░░ 15% ↻4h33m" {
		t.Errorf("5h atom = %q", got)
	}
	if got := stripANSI(atoms[1]); got != "wk ░░░░ 8% ↻3d17h" {
		t.Errorf("weekly atom = %q", got)
	}
	if got := stripANSI(atoms[2]); got != "credits ?" {
		t.Errorf("unknown-percent atom = %q", got)
	}

	// High window takes the error color.
	hot := segUsage(StatusBarParams{Theme: th, Now: now,
		UsageWindows: []provider.UsageWindow{{Label: "5h", UsedPercent: 95}}})
	if !strings.Contains(hot[0], sgrFG(th.Error)) {
		t.Errorf("95%% window should use the error color: %q", hot[0])
	}
}

func TestCostSegmentBurnRate(t *testing.T) {
	th := Dark
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	base := StatusBarParams{
		Theme:        th,
		Usage:        provider.Usage{CostUSD: 41.50},
		Subscription: true,
		Now:          now,
	}

	// No epoch: burn suppressed.
	if got := stripANSI(segCost(base)[0]); strings.Contains(got, "/hr") {
		t.Errorf("burn should be suppressed without an epoch: %q", got)
	}

	// 2h into the run with a resumed $40 baseline: only the delta burns.
	p := base
	p.SessionStart = now.Add(-2 * time.Hour)
	p.SessionCostBase = 40.00
	if got := stripANSI(segCost(p)[0]); got != "$41.500 ~$0.75/hr (sub)" {
		t.Errorf("burn atom = %q", got)
	}

	// Too early: suppressed.
	p.SessionStart = now.Add(-5 * time.Minute)
	if got := stripANSI(segCost(p)[0]); strings.Contains(got, "/hr") {
		t.Errorf("burn should be suppressed under %s: %q", burnMinElapsed, got)
	}

	// Zero cost, api key: segment absent.
	if atoms := segCost(StatusBarParams{Theme: th}); len(atoms) != 0 {
		t.Errorf("zero-cost api-key session should hide the cost segment: %q", atoms)
	}
	// Zero cost on subscription still shows (pinned behavior).
	if got := stripANSI(segCost(StatusBarParams{Theme: th, Subscription: true})[0]); got != "$0.000 (sub)" {
		t.Errorf("zero-cost subscription atom = %q", got)
	}
}

func TestFormatCountdown(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:                     "<1m",
		12 * time.Minute:                     "12m",
		4*time.Hour + 33*time.Minute:         "4h33m",
		3*24*time.Hour + 17*time.Hour:        "3d17h",
		24*time.Hour + 59*time.Second:        "1d0h",
		59*time.Minute + 59*time.Second:      "59m",
		time.Hour:                            "1h0m",
		49*24*time.Hour + 30*time.Minute + 1: "49d0h",
	} {
		if got := formatCountdown(d); got != want {
			t.Errorf("formatCountdown(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestMeterBar(t *testing.T) {
	for _, tc := range []struct {
		pct   float64
		cells int
		want  string
	}{
		{0, 5, "░░░░░"},
		{100, 5, "▓▓▓▓▓"},
		{74, 5, "▓▓▓▓░"},
		{15, 4, "▓░░░"},
		{8, 4, "░░░░"},
		{-3, 4, "░░░░"},
		{140, 4, "▓▓▓▓"},
	} {
		if got := meterBar(tc.pct, tc.cells); got != tc.want {
			t.Errorf("meterBar(%v, %d) = %q, want %q", tc.pct, tc.cells, got, tc.want)
		}
	}
}
