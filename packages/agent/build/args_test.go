package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/testsupport"
)

func TestParseArgsCWDDefaultsToGetwd(t *testing.T) {
	a, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if a.CWD != wd {
		t.Fatalf("default cwd = %q, want %q", a.CWD, wd)
	}
}

func TestParseArgsCWDAbsolutizesExistingDir(t *testing.T) {
	dir := testsupport.TempDir(t)
	// Drive a relative path through to confirm it's resolved to absolute.
	parent := filepath.Dir(dir)
	rel := filepath.Base(dir)
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}
	// Restore the original cwd so sibling tests in this package aren't
	// affected (Go runs them in the same process).
	t.Cleanup(func() { _ = os.Chdir(orig) })

	a, err := ParseArgs([]string{"--cwd", rel})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(a.CWD) {
		t.Fatalf("cwd not absolutized: %q", a.CWD)
	}
	// EvalSymlinks both sides: macOS /tmp is a symlink, so a raw string
	// compare would spuriously fail.
	got, _ := filepath.EvalSymlinks(a.CWD)
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

func TestParseArgsCWDRejectsMissingDir(t *testing.T) {
	missing := filepath.Join(testsupport.TempDir(t), "does-not-exist")
	_, err := ParseArgs([]string{"--cwd", missing})
	if err == nil {
		t.Fatal("expected error for missing --cwd dir, got nil")
	}
	if !strings.Contains(err.Error(), "--cwd") {
		t.Fatalf("error should mention --cwd: %v", err)
	}
}

func TestParseArgsContextFileRepeatablePreservesOrder(t *testing.T) {
	a, err := ParseArgs([]string{"--context-file", "a.md", "--context-file", "b.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ContextFiles) != 2 || a.ContextFiles[0] != "a.md" || a.ContextFiles[1] != "b.md" {
		t.Fatalf("context files = %v, want [a.md b.md]", a.ContextFiles)
	}
}

func TestParseArgsContextFileRequiresValue(t *testing.T) {
	_, err := ParseArgs([]string{"--context-file"})
	if err == nil {
		t.Fatal("expected error when --context-file has no value")
	}
}

func TestParseArgsTaskReadsFileIntoPrompt(t *testing.T) {
	f := filepath.Join(testsupport.TempDir(t), "task.txt")
	// Trailing newline (as an editor leaves) must be trimmed; internal newlines kept.
	if err := os.WriteFile(f, []byte("do the thing\nthen verify\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := ParseArgs([]string{"--task", f})
	if err != nil {
		t.Fatal(err)
	}
	if a.Prompt != "do the thing\nthen verify" {
		t.Fatalf("task prompt = %q, want the file body with the trailing newline trimmed", a.Prompt)
	}
	// It preloads an interactive run (not a mode change).
	if a.Mode != mode.Interactive {
		t.Errorf("--task must not change the mode, got %q", a.Mode)
	}
}

func TestParseArgsTaskMissingFileErrors(t *testing.T) {
	_, err := ParseArgs([]string{"--task", filepath.Join(testsupport.TempDir(t), "nope.txt")})
	if err == nil {
		t.Fatal("expected an error when --task names a missing file")
	}
}

func TestParseArgsTaskAndPositionalAreMutuallyExclusive(t *testing.T) {
	f := filepath.Join(testsupport.TempDir(t), "task.txt")
	if err := os.WriteFile(f, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseArgs([]string{"--task", f, "also", "positional"})
	if err == nil {
		t.Fatal("expected an error when --task is combined with a positional prompt")
	}
}

func TestParseArgsCWDRejectsFile(t *testing.T) {
	f := filepath.Join(testsupport.TempDir(t), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseArgs([]string{"--cwd", f})
	if err == nil {
		t.Fatal("expected error for --cwd pointing at a file, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error should say not a directory: %v", err)
	}
}

func TestParseArgsSwarmWorktreesFlag(t *testing.T) {
	// Absent: pointer stays nil so config decides.
	a, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.SwarmWorktrees != nil {
		t.Fatalf("SwarmWorktrees = %v; want nil when flag absent", *a.SwarmWorktrees)
	}
	// Present: pointer is non-nil and true.
	a, err = ParseArgs([]string{"--swarm-worktrees"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SwarmWorktrees == nil || !*a.SwarmWorktrees {
		t.Fatalf("SwarmWorktrees = %v; want a non-nil true", a.SwarmWorktrees)
	}
}

// Every --no-* boolean in the family parses, under every accepted spelling.
// A typo'd or dropped switch case would otherwise surface as "unknown flag"
// only when a user hits it.
func TestParseArgsNoFamilyBooleans(t *testing.T) {
	cases := []struct {
		flags []string
		field func(Args) bool
	}{
		{[]string{"--no-session"}, func(a Args) bool { return a.NoSess }},
		{[]string{"--no-tools"}, func(a Args) bool { return a.NoTools }},
		{[]string{"--no-workspace-tools"}, func(a Args) bool { return a.NoWorkspaceTools }},
		{[]string{"--no-project"}, func(a Args) bool { return a.NoProject }},
		{[]string{"--no-jail"}, func(a Args) bool { return a.NoJail }},
		{[]string{"--no-ext", "--no-extensions"}, func(a Args) bool { return a.NoExt }},
		{[]string{"--no-mcp"}, func(a Args) bool { return a.NoMCP }},
		{[]string{"--no-skill", "--no-skills"}, func(a Args) bool { return a.NoSkill }},
		{[]string{"--no-builtin-skills", "--no-builtin-skill"}, func(a Args) bool { return a.NoBuiltinSkills }},
		{[]string{"--no-always-on-skills", "--no-always-on-skill"}, func(a Args) bool { return a.NoAlwaysOnSkills }},
		{[]string{"--no-lore"}, func(a Args) bool { return a.NoLore }},
		{[]string{"--no-memory"}, func(a Args) bool { return a.NoMemory }},
		{[]string{"--no-yolo"}, func(a Args) bool { return a.NoYolo }},
	}
	for _, tc := range cases {
		for _, flag := range tc.flags {
			a, err := ParseArgs([]string{flag})
			if err != nil {
				t.Errorf("%s: %v", flag, err)
				continue
			}
			if !tc.field(a) {
				t.Errorf("%s parsed but did not set its field", flag)
			}
		}
		// Default off.
		a, err := ParseArgs(nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.field(a) {
			t.Errorf("%v: field true with no flags", tc.flags)
		}
	}
}

// --pin-skill is repeatable and adds to the config list rather than replacing
// it, so it collects into a slice like --context-file does.
func TestParseArgsPinSkillIsRepeatable(t *testing.T) {
	a, err := ParseArgs([]string{"--pin-skill", "one", "--pin-skills", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.PinSkills) != 2 || a.PinSkills[0] != "one" || a.PinSkills[1] != "two" {
		t.Fatalf("PinSkills = %v, want [one two]", a.PinSkills)
	}
	if a.NoAlwaysOnSkills {
		t.Error("--pin-skill must not imply --no-always-on-skills")
	}
}

// --pin-skill takes a value, so a bare flag at the end of the line is an error
// rather than a silent empty pin.
func TestParseArgsPinSkillNeedsAValue(t *testing.T) {
	if _, err := ParseArgs([]string{"--pin-skill"}); err == nil {
		t.Fatal("--pin-skill with no value should be an error")
	}
}

func TestParseArgsNoBuiltinSkills(t *testing.T) {
	a, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.NoBuiltinSkills {
		t.Fatal("NoBuiltinSkills should default to false")
	}
	// Both spellings, mirroring the --no-skill/--no-skills pair.
	for _, flag := range []string{"--no-builtin-skills", "--no-builtin-skill"} {
		a, err = ParseArgs([]string{flag})
		if err != nil {
			t.Fatal(err)
		}
		if !a.NoBuiltinSkills {
			t.Fatalf("%s should set NoBuiltinSkills", flag)
		}
		if a.NoSkill {
			t.Fatalf("%s must not imply NoSkill — user skills stay loaded", flag)
		}
	}
}

func TestParseArgsTUIBackendFlags(t *testing.T) {
	// The legacy direct *core.Agent TUI driver was removed; the ctrlproto
	// carrier is the only backend. The old --tui-legacy / --tui-ctrlproto
	// flags are accepted silently for backwards compatibility (no "unknown
	// flag" error) and do nothing.
	for _, flag := range []string{"--tui-legacy", "--tui-ctrlproto"} {
		if _, err := ParseArgs([]string{flag}); err != nil {
			t.Fatalf("ParseArgs(%q) errored; the deprecated flag should be accepted as a no-op: %v", flag, err)
		}
	}
	if _, err := ParseArgs([]string{"--tui-legacy", "--tui-ctrlproto"}); err != nil {
		t.Fatalf("ParseArgs with both deprecated flags errored: %v", err)
	}
}

// --web-token-file (the daemon's spelling) and --token-file (the attach client's,
// paired with --token) are two names for one field: the resolver for each mode
// reads Args.WebTokenFile, so both must land there.
func TestParseArgsTokenFileSpellings(t *testing.T) {
	for _, flag := range []string{"--web-token-file", "--token-file"} {
		a, err := ParseArgs([]string{flag, "/etc/terva/web-token"})
		if err != nil {
			t.Fatalf("ParseArgs(%q) errored: %v", flag, err)
		}
		if a.WebTokenFile != "/etc/terva/web-token" {
			t.Errorf("%s: WebTokenFile = %q, want the path to be stored", flag, a.WebTokenFile)
		}
	}
	if _, err := ParseArgs([]string{"--token-file"}); err == nil {
		t.Error("--token-file with no value should error, not silently take an empty path")
	}
}

func TestParseArgsSecretsKeyFile(t *testing.T) {
	a, err := ParseArgs([]string{"--secrets-key-file", "/etc/terva/secrets-key"})
	if err != nil {
		t.Fatalf("ParseArgs errored: %v", err)
	}
	if a.SecretsKeyFile != "/etc/terva/secrets-key" {
		t.Errorf("SecretsKeyFile = %q, want the path to be stored", a.SecretsKeyFile)
	}
	if _, err := ParseArgs([]string{"--secrets-key-file"}); err == nil {
		t.Error("--secrets-key-file with no value should error, not silently take an empty path")
	}
}

func TestResolveSwarmWorktrees(t *testing.T) {
	tru := true
	fls := false
	cases := []struct {
		name      string
		flag, cfg *bool
		want      bool
	}{
		{"both nil -> off", nil, nil, false},
		{"config on, no flag", nil, &tru, true},
		{"config off, no flag", nil, &fls, false},
		{"flag on overrides config off", &tru, &fls, true},
		{"flag off overrides config on", &fls, &tru, false},
		{"flag on, no config", &tru, nil, true},
	}
	for _, c := range cases {
		if got := ResolveSwarmWorktrees(c.flag, c.cfg); got != c.want {
			t.Errorf("%s: got %v; want %v", c.name, got, c.want)
		}
	}
}

func TestParseArgsTemperatureAllowsZero(t *testing.T) {
	args, err := ParseArgs([]string{"--temperature", "0"})
	if err != nil {
		t.Fatalf("ParseArgs returned %v", err)
	}
	if args.Temperature == nil || *args.Temperature != 0 {
		t.Fatalf("Temperature = %v; want 0", args.Temperature)
	}
}

func TestParseArgsTemperatureRejectsOutOfRange(t *testing.T) {
	if _, err := ParseArgs([]string{"--temperature", "2.1"}); err == nil {
		t.Fatal("ParseArgs accepted out-of-range temperature")
	}
}

func TestParseArgsExtensionsAndMCPAllowlists(t *testing.T) {
	a, err := ParseArgs([]string{
		"--extensions", "calendar, index",
		"--extensions", "memory",
		"--mcp", "git,jira ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.WithExtensions) != 3 || a.WithExtensions[0] != "calendar" ||
		a.WithExtensions[1] != "index" || a.WithExtensions[2] != "memory" {
		t.Fatalf("WithExtensions = %v, want [calendar index memory]", a.WithExtensions)
	}
	if len(a.WithMCP) != 2 || a.WithMCP[0] != "git" || a.WithMCP[1] != "jira" {
		t.Fatalf("WithMCP = %v, want [git jira]", a.WithMCP)
	}
	// Both flags require a value.
	if _, err := ParseArgs([]string{"--extensions"}); err == nil {
		t.Error("--extensions without a value should error")
	}
	if _, err := ParseArgs([]string{"--mcp"}); err == nil {
		t.Error("--mcp without a value should error")
	}
}

func TestParseArgsPortable(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{}, PortableOff},
		{[]string{"--portable"}, PortableOn},
		{[]string{"--portable=strict"}, PortableStrict},
	} {
		a, err := ParseArgs(c.args)
		if err != nil {
			t.Fatalf("ParseArgs(%v): %v", c.args, err)
		}
		if a.Portable != c.want {
			t.Errorf("ParseArgs(%v).Portable = %q, want %q", c.args, a.Portable, c.want)
		}
	}
	// A bare --portable must not swallow a following positional as its value.
	a, err := ParseArgs([]string{"--portable", "do the task"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if a.Portable != PortableOn {
		t.Errorf("bare --portable = %q, want on", a.Portable)
	}
	if a.Prompt != "do the task" {
		t.Errorf("--portable swallowed the positional; Prompt = %q", a.Prompt)
	}
}

func TestParseArgsRPCApprovals(t *testing.T) {
	a, err := ParseArgs([]string{"--rpc-approvals"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !a.RPCApprovals {
		t.Error("--rpc-approvals should set RPCApprovals")
	}
	b, _ := ParseArgs([]string{})
	if b.RPCApprovals {
		t.Error("RPCApprovals should default off")
	}
}

func TestParseArgsApprovalSocket(t *testing.T) {
	a, err := ParseArgs([]string{"--approval-socket", "/leases/wt-7/in.ap"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if a.ApprovalSocket != "/leases/wt-7/in.ap" {
		t.Errorf("ApprovalSocket = %q, want the socket path", a.ApprovalSocket)
	}
	b, _ := ParseArgs([]string{})
	if b.ApprovalSocket != "" {
		t.Error("ApprovalSocket should default empty (no MCP approval carrier)")
	}
}

func TestParseArgsApprovalHTTP(t *testing.T) {
	spec := `{"url":"https://orch.example/mcp","bearer_env":"ORCH_TOKEN"}`
	a, err := ParseArgs([]string{"--approval-http", spec})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if a.ApprovalHTTP != spec {
		t.Errorf("ApprovalHTTP = %q, want the descriptor verbatim", a.ApprovalHTTP)
	}
	b, _ := ParseArgs([]string{})
	if b.ApprovalHTTP != "" {
		t.Error("ApprovalHTTP should default empty (no HTTP approval carrier)")
	}
}

// The two privileged web flags are independent, which is the whole of §8.13 Q8:
// `auth` writes the credential terva uses to reach a model provider, while
// `secrets` reports on the key that opens everything, including material auth
// never touches. An operator who wants a panel that can re-authenticate an
// expired subscription has not thereby asked for one that can enumerate every
// scope in the store.
func TestWebLoginAndWebSecretsAreIndependentFlags(t *testing.T) {
	login, err := ParseArgs([]string{"--web-allow-login"})
	if err != nil {
		t.Fatal(err)
	}
	if !login.AllowWebLogin {
		t.Fatal("--web-allow-login did not set its own flag")
	}
	if login.AllowWebSecrets {
		t.Error("--web-allow-login must not grant the secrets group")
	}

	sec, err := ParseArgs([]string{"--web-allow-secrets"})
	if err != nil {
		t.Fatal(err)
	}
	if !sec.AllowWebSecrets {
		t.Fatal("--web-allow-secrets did not set its own flag")
	}
	if sec.AllowWebLogin {
		t.Error("--web-allow-secrets must not grant the auth group")
	}
}
