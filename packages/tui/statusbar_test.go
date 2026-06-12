package tui

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// TestStatusBarAlwaysTwoLines verifies the status bar always emits
// two lines when a cwd is present, regardless of terminal width, and
// that the cwd is indented with the 2-space pad so it lines up under
// the "(provider)" column on line 1.
func TestStatusBarAlwaysTwoLines(t *testing.T) {
	// Wide terminal that would previously combine into one line.
	lines := StatusBar(StatusBarParams{
		Theme:    Dark,
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		CWD:      "/tmp/x",
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
	if !strings.Contains(lines[0], "claude-opus-4-7") {
		t.Errorf("line 1 should contain model, got %q", lines[0])
	}
	// Line 2 must start with 2-space indent.
	if !strings.HasPrefix(lines[1], "  ") {
		t.Errorf("line 2 should start with 2-space indent, got %q", lines[1])
	}
	if !strings.Contains(lines[1], "/tmp/x") {
		t.Errorf("line 2 should contain cwd, got %q", lines[1])
	}
}

// TestStatusBarNoCWD verifies an empty cwd stays single-line.
func TestStatusBarNoCWD(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:    Dark,
		Provider: "openai",
		Model:    "gpt-5.4",
		CWD:      "",
		Cols:     200,
	})
	if len(lines) != 1 {
		t.Fatalf("empty cwd: want 1 line, got %d: %q", len(lines), lines)
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
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), lines)
	}
	plain := stripANSI(lines[0])
	modelIdx := strings.Index(plain, "(openai-codex) gpt-5.5")
	thinkingIdx := strings.Index(plain, "thinking: minimal")
	statsIdx := strings.Index(plain, "↑4.3M")
	if modelIdx < 0 || thinkingIdx < 0 || statsIdx < 0 {
		t.Fatalf("line should contain model, thinking level, and stats, got %q", plain)
	}
	if !(modelIdx < thinkingIdx && thinkingIdx < statsIdx) {
		t.Fatalf("thinking level should sit between model and stats, got %q", plain)
	}
}

func TestStatusBarNarrowKeepsModelAndThinkingTogetherWhenTheyFit(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:     Dark,
		Provider:  "openai-codex",
		Model:     "gpt-5.5",
		Reasoning: "maximum",
		CWD:       "/tmp/x",
		Usage: provider.Usage{
			CostUSD: 0,
		},
		Subscription: true,
		ContextUsed:  100,
		ContextMax:   1_000_000,
		Cols:         64,
	})
	if len(lines) != 3 {
		t.Fatalf("narrow status with model+thinking fit: want 3 lines, got %d: %q", len(lines), lines)
	}
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	if !strings.Contains(plain[0], "(openai-codex) gpt-5.5  thinking: maximum") {
		t.Fatalf("line 1 should contain model and thinking level, got %q", plain[0])
	}
	if !strings.Contains(plain[1], "$0.000 (sub)") || strings.Contains(plain[1], "thinking level") {
		t.Fatalf("line 2 should contain only stats, got %q", plain[1])
	}
	if !strings.Contains(plain[2], "/tmp/x") {
		t.Fatalf("line 3 should contain cwd, got %q", plain[2])
	}
}

func TestStatusBarNarrowSplitsAfterThinkingLevel(t *testing.T) {
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
		Cols: 40,
	})
	if len(lines) != 4 {
		t.Fatalf("narrow status with thinking: want 4 lines, got %d: %q", len(lines), lines)
	}
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	if !strings.Contains(plain[0], "(openai-codex) gpt-5.5") {
		t.Fatalf("line 1 should contain model info, got %q", plain[0])
	}
	if !strings.Contains(plain[1], "thinking: minimal") || strings.Contains(plain[1], "↑4.3M") {
		t.Fatalf("line 2 should contain only thinking level, got %q", plain[1])
	}
	if !strings.Contains(plain[2], "↑4.3M ↓2") {
		t.Fatalf("line 3 should contain stats, got %q", plain[2])
	}
	if !strings.Contains(plain[3], "/tmp/x") {
		t.Fatalf("line 4 should contain cwd, got %q", plain[3])
	}
}

func TestStatusBarVeryNarrowSplitsAfterThinkingLevel(t *testing.T) {
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
		Cols: 32,
	})
	if len(lines) != 4 {
		t.Fatalf("narrow status with thinking: want 4 lines, got %d: %q", len(lines), lines)
	}
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	if !strings.Contains(plain[0], "(openai-codex) gpt-5.5") {
		t.Fatalf("line 1 should contain model info, got %q", plain[0])
	}
	if !strings.Contains(plain[1], "thinking: minimal") {
		t.Fatalf("line 2 should contain thinking level, got %q", plain[1])
	}
	if !strings.Contains(plain[2], "↑4.3M ↓2") {
		t.Fatalf("line 3 should contain stats, got %q", plain[2])
	}
	if !strings.Contains(plain[3], "/tmp/x") {
		t.Fatalf("line 4 should contain cwd, got %q", plain[3])
	}
}

func TestStatusBarNoYoloTagPrecedesCWD(t *testing.T) {
	lines := StatusBar(StatusBarParams{
		Theme:    Dark,
		Provider: "openai",
		Model:    "gpt-5.5",
		CWD:      "/tmp/x",
		NoYolo:   true,
		Cols:     200,
	})
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), lines)
	}
	plain := stripANSI(lines[1])
	if !strings.Contains(plain, "yolo mode disabled - /tmp/x") {
		t.Fatalf("cwd line should include no-yolo tag before cwd, got %q", plain)
	}
}
