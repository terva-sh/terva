package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/chat"
)

// TestBotRunHelpReturnsEarly pins that `terva bot run --help` (and -h) prints
// usage and stops, instead of falling through and trying to launch a bot (which
// needs a connector + credentials). The Help check returns before the service
// is ever touched, so a zero Service is fine.
func TestBotRunHelpReturnsEarly(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		if err := botRun(chat.Service{}, []string{flag}, "test"); err != nil {
			t.Errorf("bot run %s should return nil, got %v", flag, err)
		}
	}
}

// TestNoWorkspaceToolsKeepsIntegrationsAndIdentity pins the least-privilege
// middle ground: --no-workspace-tools drops the built-in read/write/edit/bash/
// grep tools but keeps the normal identity AND still merges extension/MCP tools
// (it does NOT set noTools). That's the gap the --no-tools fix opened — a bot
// with its integrations but no host filesystem/shell.
func TestNoWorkspaceToolsKeepsIntegrationsAndIdentity(t *testing.T) {
	if a, _ := ParseArgs([]string{"--no-workspace-tools"}); !a.NoWorkspaceTools {
		t.Fatal("--no-workspace-tools should set NoWorkspaceTools")
	}
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", NoWorkspaceTools: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read", "write", "edit", "bash", "grep"} {
		if _, ok := r.ToolRegistry[name]; ok {
			t.Errorf("--no-workspace-tools should drop the built-in %q tool", name)
		}
	}
	if r.experience != "" {
		t.Errorf("--no-workspace-tools must not change identity, got experience %q", r.experience)
	}
	if r.noTools {
		t.Error("--no-workspace-tools must not set noTools (extensions/MCP still merge)")
	}
	// Extension/MCP tools still merge through the normal path.
	src := &fakeToolSource{infos: []ExtensionToolInfo{{Extension: "world", Name: "travel", Schema: []byte(`{}`)}}}
	r.MergeExtensionTools(src)
	if _, ok := r.ToolRegistry["travel"]; !ok {
		t.Error("--no-workspace-tools must keep extension/MCP tools")
	}
}

// The agent `terva bot run` builds comes straight from Resolve, so resolving
// with the same Args is a faithful check of the bot's tool + identity posture.
// (false avoids the credential requirement; the registry + system prompt are
// identical to the credentialed path bot mode uses.)

func TestChatBotHasNoToolsAndChatIdentity(t *testing.T) {
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", Experience: ExperienceChat}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToolRegistry) != 0 {
		t.Errorf("a --chat bot should have no tools, got %d: %v", len(r.ToolRegistry), toolNames(r))
	}
	if strings.Contains(r.SystemPrompt, "expert coding assistant") {
		t.Error("a --chat bot must not carry the coding-assistant identity")
	}
	if !strings.Contains(r.SystemPrompt, "conversation") {
		t.Error("a --chat bot should carry the conversational identity")
	}
}

func TestPlayBotDropsBuiltinsKeepsIdentity(t *testing.T) {
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", Experience: ExperiencePlay}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Built-in coding tools are dropped; the world's extension/MCP tools are
	// merged afterward by setupNonInteractiveExtensions (not part of Resolve).
	for _, name := range []string{"read", "write", "edit", "bash"} {
		if _, ok := r.ToolRegistry[name]; ok {
			t.Errorf("a --play bot should drop built-in tool %q", name)
		}
	}
	if !strings.Contains(r.SystemPrompt, "senses and your hands") {
		t.Error("a --play bot should carry the embodied identity")
	}
}

func TestDefaultBotIsAFullAgent(t *testing.T) {
	// No experience flag: the default bot keeps the built-in tools (and botRun
	// additionally hosts extensions + MCP), so it is not "just a chat bot".
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.ToolRegistry["bash"]; !ok {
		t.Error("the default bot should have built-in tools (read/write/edit/bash/…)")
	}
}

func TestNoExtensionsAlias(t *testing.T) {
	for _, flag := range []string{"--no-ext", "--no-extensions"} {
		a, err := ParseArgs([]string{flag})
		if err != nil || !a.NoExt {
			t.Errorf("%s should set NoExt (got NoExt=%v err=%v)", flag, a.NoExt, err)
		}
	}
	m, err := ParseArgs([]string{"--no-mcp"})
	if err != nil || !m.NoMCP {
		t.Errorf("--no-mcp should set NoMCP (got %v err=%v)", m.NoMCP, err)
	}
}

func TestBotApprovalDefault(t *testing.T) {
	// Nothing chosen → default a headless bot to yolo so it can use its tools.
	if got := botApprovalDefault("", false, ""); got != "yolo" {
		t.Errorf("bare bot should default to yolo, got %q", got)
	}
	// Any higher-precedence choice wins → leave it ("" = unchanged).
	if got := botApprovalDefault("plan", false, ""); got != "" {
		t.Errorf("explicit --approval should win, got %q", got)
	}
	if got := botApprovalDefault("", true, ""); got != "" {
		t.Errorf("--no-yolo should win, got %q", got)
	}
	if got := botApprovalDefault("", false, "workspace"); got != "" {
		t.Errorf("config approval should win, got %q", got)
	}
}

func toolNames(r Resolved) []string {
	var n []string
	for name := range r.ToolRegistry {
		n = append(n, name)
	}
	return n
}
