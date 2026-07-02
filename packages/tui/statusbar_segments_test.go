package tui

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

func TestEditsSegment(t *testing.T) {
	th := Dark
	atoms := segEdits(StatusBarParams{Theme: th, EditsAdded: 120, EditsRemoved: 45})
	if len(atoms) != 1 || stripANSI(atoms[0]) != "Δ +120 -45" {
		t.Fatalf("edits atom = %q", atoms)
	}
	if got := segEdits(StatusBarParams{Theme: th}); len(got) != 0 {
		t.Fatalf("zero edits should be absent, got %q", got)
	}
	if got := segEdits(StatusBarParams{Theme: th, EditsAdded: 5, HideWorkspace: true}); len(got) != 0 {
		t.Fatalf("edits must hide in immersive mode, got %q", got)
	}
	// One-sided counts drop the empty side.
	atoms = segEdits(StatusBarParams{Theme: th, EditsAdded: 3})
	if stripANSI(atoms[0]) != "Δ +3" {
		t.Fatalf("added-only atom = %q", stripANSI(atoms[0]))
	}
}

func TestPersonaSegment(t *testing.T) {
	th := Dark
	accent := ColorRGB(0x8f, 0xbc, 0xbb)
	atoms := segPersona(StatusBarParams{Theme: th, PersonaName: "Mieli", PersonaEmoji: "🌲", PersonaAccentRGB: &accent})
	if len(atoms) != 1 || stripANSI(atoms[0]) != "🌲 Mieli" {
		t.Fatalf("persona atom = %q", atoms)
	}
	if !strings.Contains(atoms[0], "\x1b[38;2;143;188;187m") {
		t.Fatalf("persona accent should render as exact RGB: %q", atoms[0])
	}

	// Theme status_colors override beats the accent.
	themed := th
	themed.StatusColors = map[string]int{"persona": 111}
	atoms = segPersona(StatusBarParams{Theme: themed, PersonaName: "Mieli", PersonaAccentRGB: &accent})
	if !strings.Contains(atoms[0], sgrFG(111)) {
		t.Fatalf("theme override should win over the persona accent: %q", atoms[0])
	}

	if got := segPersona(StatusBarParams{Theme: th}); len(got) != 0 {
		t.Fatalf("no persona should be absent, got %q", got)
	}
}

func TestSwarmSegment(t *testing.T) {
	th := Dark
	if got := stripANSI(segSwarm(StatusBarParams{Theme: th, SwarmAgents: 2})[0]); got != "⛭ 2 agents" {
		t.Fatalf("swarm atom = %q", got)
	}
	if got := stripANSI(segSwarm(StatusBarParams{Theme: th, SwarmAgents: 1})[0]); got != "⛭ 1 agent" {
		t.Fatalf("singular swarm atom = %q", got)
	}
	if got := segSwarm(StatusBarParams{Theme: th}); len(got) != 0 {
		t.Fatalf("empty swarm should be absent, got %q", got)
	}
	if got := segSwarm(StatusBarParams{Theme: th, SwarmAgents: 3, HideWorkspace: true}); len(got) != 0 {
		t.Fatalf("swarm must hide in immersive mode, got %q", got)
	}
}

func TestSessionAndClockSegments(t *testing.T) {
	th := Dark
	if got := stripANSI(segSession(StatusBarParams{Theme: th, SessionName: "20260702-a1b2"})[0]); got != "sess 20260702-a1b2" {
		t.Fatalf("session atom = %q", got)
	}
	if got := segSession(StatusBarParams{Theme: th}); len(got) != 0 {
		t.Fatalf("no session name should be absent, got %q", got)
	}
	now := time.Date(2026, 7, 2, 9, 5, 0, 0, time.UTC)
	if got := stripANSI(segClock(StatusBarParams{Theme: th, Now: now})[0]); got != "09:05" {
		t.Fatalf("clock atom = %q", got)
	}
}

// The immersive default rows lead with the persona instead of the raw
// provider/model pair; the regular default gains edits + swarm.
func TestDefaultRowsWithNewSegments(t *testing.T) {
	p := StatusBarParams{
		Theme:        Dark,
		Provider:     "anthropic",
		Model:        "m",
		PersonaName:  "Mieli",
		SwarmAgents:  2,
		EditsAdded:   10,
		EditsRemoved: 2,
		CWD:          "/tmp/x",
		Cols:         500,
	}
	joined := stripANSI(strings.Join(StatusBar(p), "\n"))
	for _, want := range []string{"Δ +10 -2", "⛭ 2 agents", "(anthropic) m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("regular default rows missing %q:\n%s", want, joined)
		}
	}
	// Persona is not in the regular default rows.
	if strings.Contains(joined, "Mieli") {
		t.Errorf("persona should be config-only in regular mode:\n%s", joined)
	}

	p.HideWorkspace = true
	joined = stripANSI(strings.Join(StatusBar(p), "\n"))
	if !strings.Contains(joined, "Mieli") {
		t.Errorf("immersive default rows should lead with the persona:\n%s", joined)
	}
	for _, gone := range []string{"(anthropic) m", "⛭", "Δ", "/t/x"} {
		if strings.Contains(joined, gone) {
			t.Errorf("immersive rows must not contain %q:\n%s", gone, joined)
		}
	}
}

// The default layout keeps ambient state (tags/bridge/ext) on its own
// third row so subscription meters don't cramp against it — and the
// row vanishes entirely when nothing ambient is present.
func TestDefaultThirdRowForAmbientState(t *testing.T) {
	p := StatusBarParams{
		Theme:       Dark,
		Provider:    "openai-codex",
		Model:       "gpt-5.5",
		CWD:         "/tmp/x",
		ContextUsed: 100_000, ContextMax: 272_000,
		UsageWindows: []provider.UsageWindow{
			{Label: "5h", UsedPercent: 15},
			{Label: "weekly", UsedPercent: 8},
		},
		Locked:        true,
		ChatConnected: "telegram",
		Cols:          500,
	}
	lines := StatusBar(p)
	if len(lines) != 3 {
		t.Fatalf("want 3 rows with ambient tags present, got %d: %q", len(lines), lines)
	}
	row2, row3 := stripANSI(lines[1]), stripANSI(lines[2])
	if !strings.Contains(row2, "ctx") || !strings.Contains(row2, "5h") || strings.Contains(row2, "jailed") {
		t.Errorf("row 2 should be meters only, got %q", row2)
	}
	if !strings.Contains(row3, "jailed") || !strings.Contains(row3, "telegram connected") {
		t.Errorf("row 3 should carry the ambient tags, got %q", row3)
	}

	// No ambient state: the third row collapses away.
	p.Locked = false
	p.ChatConnected = ""
	if lines = StatusBar(p); len(lines) != 2 {
		t.Fatalf("empty ambient row should vanish, got %d rows: %q", len(lines), lines)
	}
}

// Three (or more) rows are just more lists in the Rows config.
func TestThreeRowLayout(t *testing.T) {
	now := time.Date(2026, 7, 2, 9, 5, 0, 0, time.UTC)
	p := StatusBarParams{
		Theme:       Dark,
		Provider:    "openai",
		Model:       "gpt-5.5",
		CWD:         "/tmp/x",
		SessionName: "s1",
		ContextUsed: 100, ContextMax: 1000,
		Now:  now,
		Rows: [][]string{{"cwd", "model"}, {"context"}, {"session", "clock"}},
		Cols: 500,
	}
	lines := StatusBar(p)
	if len(lines) != 3 {
		t.Fatalf("want 3 rows, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(stripANSI(lines[2]), "sess s1") || !strings.Contains(stripANSI(lines[2]), "09:05") {
		t.Errorf("row 3 should carry session + clock, got %q", stripANSI(lines[2]))
	}
}
