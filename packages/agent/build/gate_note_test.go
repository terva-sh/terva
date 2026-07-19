package build

import (
	"os"
	"strings"
	"testing"
)

// TestRPCRefusalNotePointsAtCarrierFlags guards the discoverability gap: when a
// non-yolo approval mode refuses tool calls in rpc mode, the startup note must
// name the flags that fix it (--rpc-approvals / --approval-socket /
// --approval-http) rather than leave a dead end. Other headless modes, which
// have no such carrier, must NOT advertise the rpc-only flags.
func TestRPCRefusalNotePointsAtCarrierFlags(t *testing.T) {
	ask := Args{Approval: "ask"}

	out := captureStderr(t, func() { HeadlessConfirmGate(ask, "rpc") })
	for _, want := range []string{"--rpc-approvals", "--approval-socket", "--approval-http"} {
		if !strings.Contains(out, want) {
			t.Errorf("rpc refusal note should mention %s, got %q", want, out)
		}
	}

	out = captureStderr(t, func() { HeadlessConfirmGate(ask, "print") })
	if strings.Contains(out, "--rpc-approvals") {
		t.Errorf("non-rpc refusal note must not advertise rpc-only carriers, got %q", out)
	}
}

// TestApprovalCarrierFlagsDocumented guards the "parsed but undocumented" gap:
// the three headless approval-carrier flags must appear in the CLI reference so
// a driver author can discover them (the flags are accepted in args.go).
func TestApprovalCarrierFlagsDocumented(t *testing.T) {
	cli, err := os.ReadFile("../../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--rpc-approvals", "--approval-socket", "--approval-http"} {
		if !strings.Contains(string(cli), flag) {
			t.Errorf("docs/cli.md does not document %s (accepted by the arg parser)", flag)
		}
	}
}
