package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

func TestExperienceThemeFlavor(t *testing.T) {
	base := tui.Dark

	// Normal mode is unchanged (same coding flavor pools).
	if got := experienceTheme(base, ""); len(got.SpinnerMessages) != len(base.SpinnerMessages) {
		t.Errorf("normal mode should keep the default spinner messages")
	}

	chat := experienceTheme(base, ExperienceChat)
	if !equalStrs(chat.SpinnerMessages, experienceSpinnerMessages) {
		t.Errorf("chat mode should use neutral spinner messages")
	}
	if !equalStrs(chat.Greetings, chatGreetings) {
		t.Errorf("chat mode should use chat greetings")
	}
	// No coding quips leak in.
	for _, m := range chat.SpinnerMessages {
		if strings.Contains(m, "stack trace") || strings.Contains(m, "semicolon") || strings.Contains(m, "tokenizer") {
			t.Errorf("chat spinner should be code-free, got %q", m)
		}
	}

	play := experienceTheme(base, ExperiencePlay)
	if !equalStrs(play.Greetings, playGreetings) {
		t.Errorf("play mode should use play greetings")
	}

	// The original theme is not mutated (Theme is a value; we swap slice headers).
	if equalStrs(base.Greetings, chatGreetings) {
		t.Errorf("experienceTheme must not mutate the caller's theme")
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseExperienceFlags(t *testing.T) {
	chat, err := ParseArgs([]string{"--chat"})
	if err != nil || chat.Experience != ExperienceChat {
		t.Fatalf("--chat → %q, %v", chat.Experience, err)
	}
	play, err := ParseArgs([]string{"--play"})
	if err != nil || play.Experience != ExperiencePlay {
		t.Fatalf("--play → %q, %v", play.Experience, err)
	}
	if _, err := ParseArgs([]string{"--chat", "--play"}); err == nil {
		t.Fatalf("--chat --play should be mutually exclusive")
	}
	none, _ := ParseArgs([]string{})
	if none.Experience != "" {
		t.Fatalf("no flag → experience should be empty, got %q", none.Experience)
	}
}

func TestSystemPromptChatMode(t *testing.T) {
	p := BuildSystemPrompt(SystemPromptOpts{PersonaName: "Kaiku", Experience: ExperienceChat})
	if strings.Contains(p, "expert coding assistant") {
		t.Error("chat mode must drop the coding-assistant identity")
	}
	if strings.Contains(p, "prefer the edit tool") {
		t.Error("chat mode must drop the edit-tool conventions")
	}
	if !strings.Contains(p, "conversation") {
		t.Error("chat mode should frame the session as a conversation")
	}
	if !strings.Contains(p, "Kaiku") {
		t.Error("chat intro should name the persona")
	}
}

func TestSystemPromptPlayMode(t *testing.T) {
	p := BuildSystemPrompt(SystemPromptOpts{PersonaName: "Data", Experience: ExperiencePlay})
	if strings.Contains(p, "expert coding assistant") {
		t.Error("play mode must drop the coding-assistant identity")
	}
	if !strings.Contains(p, "senses and your hands") {
		t.Error("play mode should frame tools as senses and effectors")
	}
}

func TestSystemPromptExperienceCharterLayers(t *testing.T) {
	p := BuildSystemPrompt(SystemPromptOpts{
		PersonaName: "Kaiku",
		Charter:     "CHARTER-MARKER: be warm and curious.",
		Experience:  ExperienceChat,
	})
	if !strings.Contains(p, "CHARTER-MARKER") {
		t.Error("an additive charter should still layer in experience mode")
	}
	if strings.Contains(p, "expert coding assistant") {
		t.Error("experience intro should replace the coding intro under a charter too")
	}
}

func TestSystemPromptImmersiveWinsOverExperience(t *testing.T) {
	// An immersive persona sets Custom; that must win and the experience intro
	// must not appear.
	p := BuildSystemPrompt(SystemPromptOpts{
		Custom:      "I-AM-THE-WHOLE-IDENTITY.",
		PersonaName: "Data",
		Experience:  ExperiencePlay,
	})
	if !strings.Contains(p, "I-AM-THE-WHOLE-IDENTITY.") {
		t.Error("Custom (immersive) prompt should be written verbatim")
	}
	if strings.Contains(p, "senses and your hands") {
		t.Error("experience intro must not appear when Custom is set")
	}
}

func TestBuildToolRegistryDropsBuiltinsInExperienceModes(t *testing.T) {
	normal := buildToolRegistry(Args{}, core.ApprovalAutoEdit, ".", nil, "anthropic", "", false, nil)
	if len(normal) == 0 {
		t.Fatal("normal mode should have built-in tools")
	}
	for _, exp := range []string{ExperienceChat, ExperiencePlay} {
		reg := buildToolRegistry(Args{Experience: exp}, core.ApprovalAutoEdit, ".", nil, "anthropic", "", false, nil)
		if len(reg) != 0 {
			t.Errorf("%s mode should drop built-in tools, got %d", exp, len(reg))
		}
	}
}

// TestNoToolsSuppressesEveryToolSource pins the --no-tools contract for the
// now-full-agent bot: built-ins, extension tools, and MCP tools (which share
// the same merge) are ALL withheld — not just the built-ins. The skill tool is
// gated on the same !args.NoTools in buildResolved.
func TestNoToolsSuppressesEveryToolSource(t *testing.T) {
	// Built-ins: --no-tools empties the registry, like the experience modes.
	if reg := buildToolRegistry(Args{NoTools: true}, core.ApprovalAutoEdit, ".", nil, "anthropic", "", false, nil); len(reg) != 0 {
		t.Errorf("--no-tools should drop built-in tools, got %d", len(reg))
	}

	src := &fakeToolSource{infos: []ExtensionToolInfo{
		{Extension: "world", Name: "travel", Schema: []byte(`{}`)},
	}}
	// Sanity: a normal Resolved DOES merge the extension/MCP tool…
	normal := &Resolved{ToolRegistry: core.Registry{}, approvalMode: core.ApprovalAutoEdit}
	normal.MergeExtensionTools(src)
	if _, ok := normal.ToolRegistry["travel"]; !ok {
		t.Fatal("precondition: normal mode should merge extension tools")
	}
	// …but with noTools set, the merge is skipped entirely.
	none := &Resolved{ToolRegistry: core.Registry{}, approvalMode: core.ApprovalAutoEdit, noTools: true}
	none.MergeExtensionTools(src)
	if _, ok := none.ToolRegistry["travel"]; ok {
		t.Error("--no-tools must keep extension/MCP tools out of the merge")
	}
}

func TestMergeExtensionToolsChatSkipsPlayKeeps(t *testing.T) {
	src := &fakeToolSource{infos: []ExtensionToolInfo{
		{Extension: "world", Name: "travel", Schema: []byte(`{}`)},
	}}

	// chat: extension tools do NOT merge (pure conversation).
	chat := &Resolved{ToolRegistry: core.Registry{}, approvalMode: core.ApprovalAutoEdit, experience: ExperienceChat}
	chat.MergeExtensionTools(src)
	if _, ok := chat.ToolRegistry["travel"]; ok {
		t.Error("chat mode must not merge extension tools")
	}

	// play: extension tools DO merge (the simulated world).
	play := &Resolved{ToolRegistry: core.Registry{}, approvalMode: core.ApprovalAutoEdit, experience: ExperiencePlay}
	play.MergeExtensionTools(src)
	if _, ok := play.ToolRegistry["travel"]; !ok {
		t.Error("play mode must keep extension tools")
	}
}
