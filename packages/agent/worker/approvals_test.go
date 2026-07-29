package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestApprovalSocketPathDerivation pins the socket path derivation: same dir,
// ".ap" for ".sock", never longer than the inbox path (so it fits the same
// length cap) and unique wherever the inbox path is unique.
func TestApprovalSocketPathDerivation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/root/agents/abc/in.sock", "/root/agents/abc/in.ap"},             // state-dir layout: unique per id-dir
		{"/tmp/terva-swarm-1a2b/xyz.sock", "/tmp/terva-swarm-1a2b/xyz.ap"}, // tmp fallback: id in the filename, preserved
		{"/tmp/terva-swarm-1a2b/deadbeef.sock", "/tmp/terva-swarm-1a2b/deadbeef.ap"},
	}
	for _, c := range cases {
		got := approvalSocketPath(c.in)
		if got != c.want {
			t.Errorf("approvalSocketPath(%q) = %q, want %q", c.in, got, c.want)
		}
		if len(got) > len(c.in) {
			t.Errorf("approval path %q is longer than the inbox path %q — could break the length cap", got, c.in)
		}
	}
}

// TestServeApprovalsRoutesToConfirmer is the MCP carrier's serving half: a
// Request dialed onto the socket reaches the orchestrator's Confirmer (labelled
// with the worker id so the human knows whose action it is) and the verdict
// comes back on the same connection. The socket is 0600 — perms are the auth.
func TestServeApprovalsRoutesToConfirmer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	var sawTool, sawPreview string
	r := &Runner{
		agent: &swarm.Agent{ID: "w-7"},
		confirmer: confirmFunc(func(_ context.Context, tool, preview string) core.ConfirmDecision {
			sawTool, sawPreview = tool, preview
			return core.ConfirmDecision{Allow: true}
		}),
	}
	sock := filepath.Join(shortSocketDir(t), "a.ap")
	al, err := r.serveApprovals(context.Background(), sock)
	if err != nil {
		t.Fatalf("serveApprovals: %v", err)
	}
	defer al.Close()

	// Perms are the auth: no group/world access.
	if fi, serr := os.Stat(sock); serr == nil && fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("socket perms = %o, want no group/world access (perms are the auth)", fi.Mode().Perm())
	}

	reply, err := dialApproval(sock, mcpbridge.Request{Tool: "bash", Preview: "rm -rf x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if !reply.Allow {
		t.Errorf("reply = %+v, want allow", reply)
	}
	if sawTool != "bash" {
		t.Errorf("confirmer saw tool %q, want bash", sawTool)
	}
	if !strings.Contains(sawPreview, "worker w-7:") || !strings.Contains(sawPreview, "rm -rf x") {
		t.Errorf("card preview %q must name the worker and the args", sawPreview)
	}
}

// TestServeApprovalsDeniesWithNoApprover: a worker whose orchestrator is gone
// (nil confirmer) gets a clean deny over the socket — the same safe fallback the
// rpc-native carrier gives, so the MCP carrier can't freeze a gated worker.
func TestServeApprovalsDeniesWithNoApprover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	r := &Runner{agent: &swarm.Agent{ID: "orphan"}, confirmer: nil}
	sock := filepath.Join(shortSocketDir(t), "a.ap")
	al, err := r.serveApprovals(context.Background(), sock)
	if err != nil {
		t.Fatalf("serveApprovals: %v", err)
	}
	defer al.Close()

	reply, err := dialApproval(sock, mcpbridge.Request{Tool: "bash", Preview: "x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if reply.Allow {
		t.Errorf("a worker with no approver must be denied, got %+v", reply)
	}
}

// TestServeApprovalsDeniesMalformed: an unparseable request is denied (fail
// closed) with a verdict, not dropped — so the bridge gets an answer to return
// rather than hanging. Even an allow-everything confirmer can't rescue it,
// because decide is never reached.
func TestServeApprovalsDeniesMalformed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	r := &Runner{
		agent: &swarm.Agent{ID: "w"},
		confirmer: confirmFunc(func(context.Context, string, string) core.ConfirmDecision {
			return core.ConfirmDecision{Allow: true}
		}),
	}
	sock := filepath.Join(shortSocketDir(t), "a.ap")
	al, err := r.serveApprovals(context.Background(), sock)
	if err != nil {
		t.Fatalf("serveApprovals: %v", err)
	}
	defer al.Close()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, _ := bufio.NewReader(conn).ReadBytes('\n')
	var reply mcpbridge.Reply
	if err := json.Unmarshal(bytes.TrimSpace(line), &reply); err != nil {
		t.Fatalf("reply not json: %q", line)
	}
	if reply.Allow {
		t.Errorf("a malformed request must be denied, got %+v", reply)
	}
}

// TestServeApprovalsCancelDenies: if the worker is stopped while its approval is
// parked on a human, handleApprovalConn unwinds (denying) instead of blocking
// the connection forever — the socket carrier's copy of the rpc carrier's
// teardown-safety property.
func TestServeApprovalsCancelDenies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	blocked := make(chan struct{})
	r := &Runner{
		agent: &swarm.Agent{ID: "slow"},
		confirmer: confirmFunc(func(ctx context.Context, _, _ string) core.ConfirmDecision {
			close(blocked)
			// The human walked away. The worker's context is what ends the
			// wait — a Confirmer that parks is required to honour it.
			<-ctx.Done()
			return core.ConfirmDecision{Allow: false, Reason: "worker stopped before the approval was answered"}
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	sock := filepath.Join(shortSocketDir(t), "a.ap")
	al, err := r.serveApprovals(ctx, sock)
	if err != nil {
		t.Fatalf("serveApprovals: %v", err)
	}
	defer al.Close()

	replyCh := make(chan mcpbridge.Reply, 1)
	go func() {
		reply, _ := dialApproval(sock, mcpbridge.Request{Tool: "bash", Preview: "y"})
		replyCh <- reply
	}()

	<-blocked
	cancel() // the worker is stopped
	select {
	case reply := <-replyCh:
		if reply.Allow {
			t.Errorf("a stopped worker's approval must deny, got %+v", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleApprovalConn did not unwind when the worker was stopped")
	}
}

// TestRunServesApprovalSocketForBackend proves the runner's Run wiring: for a
// backend with ApprovalSocket set, Run opens and serves the socket BEFORE
// building the command and hands its path to Command. The fake backend plays the
// worker's bridge — dialing the socket synchronously inside Command (which runs
// after serveApprovals) — so the whole path (path derivation, listen, accept,
// decide, Dispatch plumbing) is exercised without a real MCP subprocess.
func TestRunServesApprovalSocketForBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	resolved := loadedRepo(t)

	var gotSocket string
	var gotReply mcpbridge.Reply
	var gotErr error
	backend := Backend{
		Name:           "fake-ap",
		SelfAssembles:  true, // skip the scrub; composition isn't what this tests
		ApprovalSocket: true,
		Translate:      func([]byte) []Event { return nil },
		Command: func(d Dispatch) (*exec.Cmd, error) {
			gotSocket = d.ApprovalSocket
			if d.ApprovalSocket != "" {
				// serveApprovals already ran, so the socket is up: dial it now,
				// synchronously, as the worker's bridge would.
				gotReply, gotErr = dialApproval(d.ApprovalSocket, mcpbridge.Request{Tool: "bash", Preview: "make test"})
			}
			return exec.Command("true"), nil // a child that exits immediately
		},
	}
	confirmer := confirmFunc(func(context.Context, string, string) core.ConfirmDecision {
		return core.ConfirmDecision{Allow: true}
	})

	a := &swarm.Agent{
		ID:           "ap-run-1",
		Dir:          testsupport.TempDir(t),
		InboxPath:    filepath.Join(shortSocketDir(t), "in.sock"),
		EventLogPath: filepath.Join(testsupport.TempDir(t), "events.jsonl"),
	}
	r := NewRunner(a, backend, resolved, confirmer)
	if err := r.Run(context.Background(), nopSink{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if gotSocket == "" {
		t.Fatal("Command received no ApprovalSocket path — Run did not wire the socket")
	}
	if want := approvalSocketPath(a.InboxPath); gotSocket != want {
		t.Errorf("ApprovalSocket = %q, want %q (inbox path with .ap)", gotSocket, want)
	}
	if gotErr != nil {
		t.Fatalf("the worker could not reach the approval socket: %v", gotErr)
	}
	if !gotReply.Allow {
		t.Errorf("approval reply = %+v, want allow", gotReply)
	}
}

// dialApproval dials the approval socket, sends one Request, and reads the
// Reply. It never touches *testing.T, so it is safe to call from a goroutine.
func dialApproval(sock string, req mcpbridge.Request) (mcpbridge.Reply, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return mcpbridge.Reply{}, err
	}
	defer conn.Close()
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return mcpbridge.Reply{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if len(bytes.TrimSpace(line)) == 0 && err != nil {
		return mcpbridge.Reply{}, err
	}
	var reply mcpbridge.Reply
	if uerr := json.Unmarshal(bytes.TrimSpace(line), &reply); uerr != nil {
		return mcpbridge.Reply{}, uerr
	}
	return reply, nil
}

// shortSocketDir returns a tmp dir short enough to hold a unix socket under the
// ~104-byte cap (the runner's default tmp is too long on macOS). Mirrors the
// swarm inbox tests' helper.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "terva-ap-")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
