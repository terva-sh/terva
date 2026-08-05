package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The wire mode exists to be diffed across two transcript states. That is only
// possible if it reflects the transcript at all -- and --dump-prompt did not
// read --session, so every mode emitted the prompt for turn 1 of a conversation
// that might be on turn 80. Dumping the same bytes for both is worse than
// useless for a prefix comparison: it reports "no change" for every change.
func TestPromptDumpWireReflectsTheSessionTranscript(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	sess, err := core.NewSession(home, home, "openai-codex", "gpt-5.6-terra", "test")
	if err != nil {
		t.Fatal(err)
	}
	// A user/assistant pair, not a bare user turn: the codex builder merges
	// same-role adjacency (openai_codex.go), so a seeded user message followed
	// by the pending user turn would coalesce into ONE input item and the test
	// would be asserting against a transcript shape no session produces.
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "a seeded turn from the session"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a seeded reply"}}},
	} {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	args := build.Args{DumpPrompt: "wire", Prompt: "the pending turn",
		Provider: "openai-codex", Model: "gpt-5.6-terra", Session: sess.Path}
	out, err := promptDumpText(args)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("want a header plus the seeded turn plus the pending turn, got %d lines:\n%s", len(lines), out)
	}
	for i, ln := range lines {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("line %d is not JSON, so the output is not diffable JSONL: %s", i, ln)
		}
	}
	if !strings.Contains(out, "a seeded turn from the session") {
		t.Error("the dump does not carry the session transcript")
	}
	if !strings.Contains(lines[len(lines)-1], "the pending turn") {
		t.Errorf("the pending turn must be the LAST item, so a diff against a later\n"+
			"dump shows it replaced by what really followed; got:\n%s", lines[len(lines)-1])
	}
	// The cache key is a per-session value; a dump that has a session should
	// show it rather than leave a reader guessing which conversation this was.
	if !strings.Contains(lines[0], "prompt_cache_key") {
		t.Errorf("header should carry the session's prompt_cache_key:\n%s", lines[0])
	}
}

// An unsupported provider must fail loudly rather than emit an empty or
// half-built dump that would read as "this request carries nothing".
func TestPromptDumpWireRefusesAProviderItCannotSerialize(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	args := build.Args{DumpPrompt: "wire", Prompt: "hi",
		Provider: "anthropic", Model: "claude-haiku-4-5-20251001"}
	if out, err := promptDumpText(args); err == nil {
		t.Fatalf("want an error for a provider with no wire dumper, got:\n%s", out)
	}
}
