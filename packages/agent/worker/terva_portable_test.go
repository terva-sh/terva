package worker

import (
	"strings"
	"testing"
)

// flagValue returns the argument that follows flag in argv, or "".
func flagValue(argv []string, flag string) string {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// TestPortableBriefingByteIdenticalAcrossBackends is THE sufficiency-test claim:
// claude and terva:portable, given the same composed briefing, carry
// byte-identical text — the same identity string and the same opening turn —
// and only the CARRIER differs (--append-system-prompt + a stream-json frame
// there; --system-prompt + an rpc prompt here). Byte-for-byteness is structural:
// both route through the same Compose and the same foreign Opening, so there is
// no second path to drift. If this ever fails, terva:portable stopped being a
// faithful stand-in and the conformance run it anchors is meaningless.
func TestPortableBriefingByteIdenticalAcrossBackends(t *testing.T) {
	r := loadedRepo(t) // charter + AGENTS.md + context file + user append: a maximal briefing
	b := Compose(r, demoTask(), Workspace{Path: "/lease/wt-1", Branch: "work/x"})

	claude, err := Lookup(BackendClaude)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := Lookup(BackendTervaPortable)
	if err != nil {
		t.Fatal(err)
	}
	// Both must go through the foreign composer — a SelfAssembles mismatch would
	// mean terva:portable isn't the control it claims to be.
	if claude.SelfAssembles || tp.SelfAssembles {
		t.Fatalf("both backends must be config-opaque: claude=%v terva:portable=%v", claude.SelfAssembles, tp.SelfAssembles)
	}

	d := Dispatch{Briefing: b, Dir: "/lease/wt-1"}
	claudeCmd, err := claude.Command(d)
	if err != nil {
		t.Fatal(err)
	}
	tpCmd, err := tp.Command(d)
	if err != nil {
		t.Fatal(err)
	}

	// 1. The identity each carries in its system-prompt flag is the same string.
	claudeIdentity := flagValue(claudeCmd.Args, "--append-system-prompt")
	tpIdentity := flagValue(tpCmd.Args, "--system-prompt")
	if claudeIdentity == "" {
		t.Fatal("claude carried no identity on --append-system-prompt")
	}
	if claudeIdentity != tpIdentity {
		t.Errorf("identity differs across backends:\nclaude --append-system-prompt: %q\nterva:portable --system-prompt: %q", claudeIdentity, tpIdentity)
	}

	// 2. The opening turn each sends is the same string.
	if claude.Opening(b) != tp.Opening(b) {
		t.Errorf("opening turn differs:\nclaude: %q\nterva:portable: %q", claude.Opening(b), tp.Opening(b))
	}

	// 3. The union — identity + opening — is EXACTLY the composed briefing text:
	// nothing added, nothing lost, for both carriers.
	want := b.Text()
	if got := join2(claudeIdentity, claude.Opening(b)); got != want {
		t.Errorf("claude's carried text != Briefing.Text():\n got: %q\nwant: %q", got, want)
	}
	if got := join2(tpIdentity, tp.Opening(b)); got != want {
		t.Errorf("terva:portable's carried text != Briefing.Text():\n got: %q\nwant: %q", got, want)
	}
}

// TestTervaPortableWiresApprovalSocket: terva:portable gates through the MCP
// bridge, so it declares ApprovalSocket (the runner serves a socket) and points
// --approval-socket at that socket — the identical carrier claude reaches via
// --permission-prompt-tool. The config-TRANSITIVE dogfood backend does NOT: it
// asks over the rpc-native wire (--rpc-approvals), so it must not claim the
// socket.
func TestTervaPortableWiresApprovalSocket(t *testing.T) {
	if !tervaPortableBackend().ApprovalSocket {
		t.Error("terva:portable must opt into the approval socket so the runner serves one")
	}
	if tervaBackend().ApprovalSocket {
		t.Error("the dogfood terva backend uses the rpc-native carrier, not the MCP socket")
	}

	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/lease/wt-3"})

	cmd, err := tervaPortableCommand(Dispatch{Briefing: b, Dir: "/lease/wt-3", ApprovalSocket: "/lease/wt-3/in.ap"})
	if err != nil {
		t.Fatal(err)
	}
	if v := flagValue(cmd.Args, "--approval-socket"); v != "/lease/wt-3/in.ap" {
		t.Errorf("--approval-socket = %q, want the served socket path", v)
	}

	// No socket served -> no flag, so the worker keeps its refuse-by-default.
	bare, err := tervaPortableCommand(Dispatch{Briefing: b, Dir: "/lease/wt-3"})
	if err != nil {
		t.Fatal(err)
	}
	if v := flagValue(bare.Args, "--approval-socket"); v != "" {
		t.Errorf("no served socket must mean no --approval-socket, got %q", v)
	}
}

func join2(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n\n" + b
	}
}

// TestTervaPortableCommandDiffersFromDogfood pins the two ways the portable
// carrier differs from the config-transitive one: the identity rides
// --system-prompt (not --persona), --portable is present (so the worker strips
// its own self-context), and the task is never on argv.
func TestTervaPortableCommandDiffersFromDogfood(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, Task{Mission: "fix the flaky test"}, Workspace{Path: "/lease/wt-2"})
	b.Route = Route{Provider: "anthropic", Model: "claude-sonnet-5"}

	cmd, err := tervaPortableCommand(Dispatch{Briefing: b, Dir: "/lease/wt-2"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(cmd.Args, " ")

	if !strings.Contains(argv, "--portable") {
		t.Errorf("portable worker must run --portable: %s", argv)
	}
	if !strings.Contains(argv, "--system-prompt") {
		t.Errorf("identity must ride --system-prompt: %s", argv)
	}
	if strings.Contains(argv, "--persona") {
		t.Errorf("a config-OPAQUE worker must NOT self-assemble a persona: %s", argv)
	}
	if strings.Contains(argv, "fix the flaky test") {
		t.Errorf("the task must ride stdin, not argv: %s", argv)
	}
	if v := flagValue(cmd.Args, "--system-prompt"); v != b.Identity.travellingText() {
		t.Errorf("--system-prompt = %q, want the composed identity", v)
	}
}
