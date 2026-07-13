package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func textMsg(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func compactionMsg(summary string) provider.Message {
	m := textMsg(provider.RoleUser, "## Context Summary (compacted)\n\n"+summary)
	m.Meta = map[string]string{"compaction": "true"}
	return m
}

// --- ClipMiddle ---

func TestClipMiddleShortStringUntouched(t *testing.T) {
	if got := ClipMiddle("hello world", 50); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestClipMiddleKeepsHeadAndTail(t *testing.T) {
	s := "START the quick brown fox jumps over the lazy dog again and again and again END"
	got := ClipMiddle(s, 40)
	if !strings.Contains(got, "START") || !strings.Contains(got, "END") {
		t.Fatalf("head/tail lost: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("no elision marker: %q", got)
	}
	if n := len([]rune(got)); n > 40 {
		t.Fatalf("budget exceeded: %d runes: %q", n, got)
	}
}

// The marker counts against the budget and cuts are rune-safe: a multi-byte
// string clipped at any budget must survive a UTF-8 round trip un-mangled.
func TestClipMiddleRuneSafe(t *testing.T) {
	s := strings.Repeat("héllo wörld — päckage tïtle ", 40)
	for budget := 1; budget < 120; budget += 7 {
		got := ClipMiddle(s, budget)
		if n := len([]rune(got)); n > budget {
			t.Fatalf("budget %d exceeded: %d runes", budget, n)
		}
		if strings.ContainsRune(got, '�') {
			t.Fatalf("budget %d produced replacement char: %q", budget, got)
		}
	}
}

func TestClipMiddleTinyBudgetHeadCut(t *testing.T) {
	got := ClipMiddle("abcdefghij", 5)
	if got != "abcde" {
		t.Fatalf("tiny budget should head-cut, got %q", got)
	}
}

// --- BuildTitleSeed ---

// First-exchange degenerate case: no compaction, one user message → the seed
// is that message under the opening label, no recency section (today's
// settleTitle behavior, cascaded).
func TestSeedFirstExchangeDegenerates(t *testing.T) {
	msgs := []provider.Message{textMsg(provider.RoleUser, "help me refactor the parser")}
	got := BuildTitleSeed(msgs, TitleSeedBudget)
	want := "The conversation opens with:\nhelp me refactor the parser"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSeedAnchorsOnFirstUserMessageWithRecents(t *testing.T) {
	msgs := []provider.Message{
		textMsg(provider.RoleUser, "help me refactor the parser"),
		textMsg(provider.RoleAssistant, "sure — which module?"),
		textMsg(provider.RoleUser, "the tokenizer, it chokes on unicode"),
	}
	got := BuildTitleSeed(msgs, TitleSeedBudget)
	if !strings.HasPrefix(got, "The conversation opens with:\nhelp me refactor the parser") {
		t.Fatalf("anchor wrong: %q", got)
	}
	if !strings.Contains(got, "Most recent exchanges:") {
		t.Fatalf("no recency section: %q", got)
	}
	// Chronological order and role prefixes; the anchor message itself is
	// not duplicated into the recency section.
	iAssistant := strings.Index(got, "assistant: sure — which module?")
	iUser := strings.Index(got, "user: the tokenizer, it chokes on unicode")
	if iAssistant == -1 || iUser == -1 || iAssistant > iUser {
		t.Fatalf("recents wrong or out of order: %q", got)
	}
	if strings.Count(got, "help me refactor the parser") != 1 {
		t.Fatalf("anchor duplicated in recents: %q", got)
	}
}

func TestSeedAnchorsOnLatestCompaction(t *testing.T) {
	msgs := []provider.Message{
		textMsg(provider.RoleUser, "original opening ask"),
		compactionMsg("first checkpoint: built the lexer"),
		textMsg(provider.RoleUser, "mid work"),
		compactionMsg("second checkpoint: lexer done, parser next"),
		textMsg(provider.RoleUser, "now add error recovery"),
		textMsg(provider.RoleAssistant, "adding panic-mode recovery"),
	}
	got := BuildTitleSeed(msgs, TitleSeedBudget)
	if !strings.HasPrefix(got, "Summary of the session so far:\nsecond checkpoint: lexer done, parser next") {
		t.Fatalf("should anchor on the LATEST compaction with header stripped: %q", got)
	}
	if strings.Contains(got, "first checkpoint") || strings.Contains(got, "## Context Summary") {
		t.Fatalf("stale checkpoint or raw header leaked: %q", got)
	}
	if !strings.Contains(got, "user: now add error recovery") || !strings.Contains(got, "assistant: adding panic-mode recovery") {
		t.Fatalf("post-compaction recents missing: %q", got)
	}
	if strings.Contains(got, "mid work") {
		t.Fatalf("pre-compaction message leaked into recents: %q", got)
	}
}

// A huge compaction must not starve the recency section: the floor holds.
func TestSeedRecencyFloorHolds(t *testing.T) {
	msgs := []provider.Message{
		compactionMsg(strings.Repeat("the summary drones on and on. ", 2000)), // ~60k chars
		textMsg(provider.RoleUser, "the real current focus: fix the flaky bridge reconnect race"),
	}
	got := BuildTitleSeed(msgs, 1000)
	if !strings.Contains(got, "fix the flaky bridge reconnect race") {
		t.Fatalf("recency starved by a big compaction: %q", got)
	}
	// The anchor was middle-out clipped, not dropped.
	if !strings.Contains(got, "Summary of the session so far:") || !strings.Contains(got, "truncated") {
		t.Fatalf("anchor missing or unclipped: %q", got)
	}
}

// Per-message clamp: one giant recent message must not consume the whole
// recency allowance — breadth beats depth.
func TestSeedPerMessageClamp(t *testing.T) {
	msgs := []provider.Message{
		textMsg(provider.RoleUser, "opening ask"),
		textMsg(provider.RoleAssistant, strings.Repeat("giant paste ", 500)),
		textMsg(provider.RoleUser, "small follow-up one"),
		textMsg(provider.RoleUser, "small follow-up two"),
	}
	got := BuildTitleSeed(msgs, TitleSeedBudget)
	for _, want := range []string{"small follow-up one", "small follow-up two"} {
		if !strings.Contains(got, want) {
			t.Fatalf("small recents crowded out: missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("giant message not clamped: %q", got)
	}
}

// Tool traffic and non-chat roles carry no title signal and stay out.
func TestSeedSkipsToolNoise(t *testing.T) {
	msgs := []provider.Message{
		textMsg(provider.RoleUser, "opening ask"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: "t1", Name: "bash", Arguments: json.RawMessage(`{"command":"SECRET-tool-arg"}`)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{
			provider.ToolResultBlock{CallID: "t1", Content: []provider.Content{provider.TextBlock{Text: "SECRET-tool-output"}}},
		}},
		textMsg(provider.RoleAssistant, "the tests pass now"),
	}
	got := BuildTitleSeed(msgs, TitleSeedBudget)
	if strings.Contains(got, "SECRET") {
		t.Fatalf("tool traffic leaked into the seed: %q", got)
	}
	if !strings.Contains(got, "assistant: the tests pass now") {
		t.Fatalf("assistant text lost: %q", got)
	}
}

func TestSeedEmptyTranscript(t *testing.T) {
	if got := BuildTitleSeed(nil, TitleSeedBudget); got != "" {
		t.Fatalf("empty transcript should yield empty seed, got %q", got)
	}
	onlyTools := []provider.Message{{Role: provider.RoleTool, Content: []provider.Content{provider.ToolResultBlock{CallID: "x"}}}}
	if got := BuildTitleSeed(onlyTools, TitleSeedBudget); got != "" {
		t.Fatalf("tool-only transcript should yield empty seed, got %q", got)
	}
}

// The seed's content stays within the same order of magnitude as the budget
// across shapes (labels/separators ride on top; content is what's governed).
func TestSeedRespectsBudgetApproximately(t *testing.T) {
	var msgs []provider.Message
	msgs = append(msgs, compactionMsg(strings.Repeat("summary ", 5000)))
	for i := 0; i < 200; i++ {
		msgs = append(msgs, textMsg(provider.RoleUser, strings.Repeat(fmt.Sprintf("recent %d ", i), 60)))
	}
	const budget = 2000
	got := BuildTitleSeed(msgs, budget)
	if n := len([]rune(got)); n > budget+200 { // labels + separators only
		t.Fatalf("seed blew the budget: %d runes for budget %d", n, budget)
	}
}
