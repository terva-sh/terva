package tui

// Tests for ToolDisplayMode: the ctrl+t cycle between full bordered
// boxes, one-line minimal summaries, and fully hidden tool calls.
// Immersive chat/play sessions default to minimal so tool mechanics
// stay out of the conversation; ctrl+o (ExpandAll) must always win as
// the recovery hatch.

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// finishedToolView builds a transcript with one completed bash call
// whose result spans several lines, plus a trailing assistant summary.
func finishedToolView(isErr bool) View {
	args := json.RawMessage(`{"command":"go test ./..."}`)
	return View{
		Theme: Dark,
		Messages: []provider.Message{
			{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "toolu_1", Name: "bash", Arguments: args}},
			},
			{
				Role: provider.RoleTool,
				Content: []provider.Content{provider.ToolResultBlock{
					CallID:  "toolu_1",
					IsError: isErr,
					Content: []provider.Content{provider.TextBlock{Text: "$ go test ./...\nok\nok\nFAIL"}},
				}},
			},
			{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "tests ran."}},
			},
		},
	}
}

func TestToolDisplayMinimalReplacesBoxWithOneLine(t *testing.T) {
	v := finishedToolView(false)
	v.ToolDisplay = ToolDisplayMinimal
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if strings.ContainsAny(plain, "┌└│") {
		t.Fatalf("minimal display still renders box borders:\n%s", plain)
	}
	if !strings.Contains(plain, "· bash go test ./...") {
		t.Fatalf("minimal summary line missing:\n%s", plain)
	}
	if !strings.Contains(plain, "4 lines") {
		t.Fatalf("minimal summary should size the result body:\n%s", plain)
	}
	if strings.Contains(plain, "FAIL") {
		t.Fatalf("minimal display must not render the result body:\n%s", plain)
	}
	if !strings.Contains(plain, "tests ran.") {
		t.Fatalf("assistant prose must survive minimal tool display:\n%s", plain)
	}
}

func TestToolDisplayHiddenDropsToolCalls(t *testing.T) {
	v := finishedToolView(false)
	v.ToolDisplay = ToolDisplayHidden
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if strings.Contains(plain, "bash") || strings.ContainsAny(plain, "┌└│") {
		t.Fatalf("hidden display leaked tool content:\n%s", plain)
	}
	if !strings.Contains(plain, "tests ran.") {
		t.Fatalf("assistant prose must survive hidden tool display:\n%s", plain)
	}
}

// A failed call must stay visible even in hidden mode — reduced
// display tucks mechanics away, it must not swallow failures.
func TestToolDisplayHiddenStillSurfacesErrors(t *testing.T) {
	v := finishedToolView(true)
	v.ToolDisplay = ToolDisplayHidden
	plain := stripANSI(strings.Join(v.Build(80), "\n"))
	if !strings.Contains(plain, "× bash go test ./...") {
		t.Fatalf("hidden display swallowed a failed tool call:\n%s", plain)
	}
}

// ctrl+o's ExpandAll overrides any reduced mode with full boxes, so
// minimal/hidden output is always one keystroke from recoverable.
func TestExpandAllOverridesReducedToolDisplay(t *testing.T) {
	for _, mode := range []ToolDisplayMode{ToolDisplayMinimal, ToolDisplayHidden} {
		v := finishedToolView(false)
		v.ToolDisplay = mode
		v.ExpandAll = true
		plain := stripANSI(strings.Join(v.Build(80), "\n"))
		if !strings.Contains(plain, "FAIL") || !strings.Contains(plain, "│") {
			t.Fatalf("ExpandAll must restore full boxes over %v:\n%s", mode, plain)
		}
	}
}

// The live in-flight overlay follows the same mode: minimal tracks the
// call with one line, hidden shows nothing (the spinner covers it).
func TestLiveOverlayFollowsToolDisplay(t *testing.T) {
	args := json.RawMessage(`{"command":"sleep 5"}`)
	mk := func(mode ToolDisplayMode) View {
		return View{
			Theme: Dark,
			Messages: []provider.Message{{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "toolu_1", Name: "bash", Arguments: args}},
			}},
			ToolCalls:   []ToolCallView{{ID: "toolu_1", Name: "bash", Args: ShortArgs("bash", args)}},
			ToolDisplay: mode,
		}
	}

	vMin := mk(ToolDisplayMinimal)
	plain := stripANSI(strings.Join(vMin.Build(80), "\n"))
	if !strings.Contains(plain, "· bash sleep 5 — running…") {
		t.Fatalf("live minimal line missing:\n%s", plain)
	}
	if strings.ContainsAny(plain, "┌└│") {
		t.Fatalf("live minimal display still renders a box:\n%s", plain)
	}

	vHid := mk(ToolDisplayHidden)
	plain = stripANSI(strings.Join(vHid.Build(80), "\n"))
	if strings.Contains(plain, "bash") {
		t.Fatalf("live hidden display leaked the tool call:\n%s", plain)
	}
}
