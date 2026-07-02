package tui

import (
	"strings"
	"testing"
)

// Script segments render where Rows places them; plain output takes
// the muted tint, styled output passes through with a closing reset.
func TestScriptSegmentInRows(t *testing.T) {
	p := StatusBarParams{
		Theme:    Dark,
		Provider: "openai",
		Model:    "m",
		ScriptSegments: map[string]string{
			"weather": "72°F sunny",
			"styled":  "\x1b[1;35mmoon phase: waxing\x1b[0m",
		},
		Rows: [][]string{{"model", "weather", "styled"}},
		Cols: 500,
	}
	lines := StatusBar(p)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %q", lines)
	}
	plain := stripANSI(lines[0])
	for _, want := range []string{"(openai) m", "72°F sunny", "moon phase: waxing"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in %q", want, plain)
		}
	}
	if !strings.Contains(lines[0], "\x1b[1;35m") {
		t.Errorf("styled script output should keep its SGR: %q", lines[0])
	}
}

// With no Rows config, defined scripts append to the last default row.
func TestScriptSegmentsAppendToDefaultRows(t *testing.T) {
	p := StatusBarParams{
		Theme:          Dark,
		Provider:       "openai",
		Model:          "m",
		Locked:         true, // ambient row present
		ScriptSegments: map[string]string{"weather": "72°F"},
		Cols:           500,
	}
	lines := StatusBar(p)
	last := stripANSI(lines[len(lines)-1])
	if !strings.Contains(last, "jailed") || !strings.Contains(last, "72°F") {
		t.Fatalf("script should append to the ambient row, got %q (all: %q)", last, lines)
	}
}

// A script named after a built-in loses: the built-in renders, the
// script output does not.
func TestScriptSegmentBuiltinNameWins(t *testing.T) {
	p := StatusBarParams{
		Theme:          Dark,
		Provider:       "openai",
		Model:          "m",
		ScriptSegments: map[string]string{"model": "evil override"},
		Rows:           [][]string{{"model"}},
		Cols:           500,
	}
	joined := stripANSI(strings.Join(StatusBar(p), "\n"))
	if strings.Contains(joined, "evil override") || !strings.Contains(joined, "(openai) m") {
		t.Fatalf("built-in must win a name collision: %q", joined)
	}
}

// Script output is sanitized before embedding: cursor-moving escapes,
// control bytes, and extra lines are dropped; tabs expand; SGR stays.
func TestSanitizeStatusScriptLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                        "plain",
		"two\nlines":                   "two",
		"crlf\r\nrest":                 "crlf",
		"tab\there":                    "tab  here",
		"\x1b[31mred\x1b[0m":           "\x1b[31mred\x1b[0m",
		"move\x1b[2Aup":                "moveup",
		"osc\x1b]0;title\x07done":      "oscdone",
		"bell\x07 clean":               "bell clean",
		"  padded  ":                   "padded",
		"\x1b[1;35mbold magenta\x1b[m": "\x1b[1;35mbold magenta\x1b[m",
	} {
		if got := sanitizeStatusScriptLine(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
