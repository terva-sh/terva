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
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

func TestTeamSocketPathDerivation(t *testing.T) {
	for _, in := range []string{"/root/agents/abc/in.sock", "/tmp/terva-swarm-1a2b/xyz.sock"} {
		got := teamSocketPath(in)
		if got != strings.TrimSuffix(in, ".sock")+".tk" {
			t.Errorf("teamSocketPath(%q) = %q", in, got)
		}
		if len(got) > len(in) || got == approvalSocketPath(in) {
			t.Errorf("team path %q must fit the inbox cap and differ from the approval path", got)
		}
	}
}

// teamAgent is an agent whose sockets fit the unix length cap.
func teamAgent(t *testing.T, id string) *swarm.Agent {
	t.Helper()
	return &swarm.Agent{
		ID:           id,
		Dir:          testsupport.TempDir(t),
		InboxPath:    filepath.Join(shortSocketDir(t), "in.sock"),
		EventLogPath: filepath.Join(testsupport.TempDir(t), "events.jsonl"),
	}
}

// With a Team and a TeamBridge backend, Run serves a 0600 socket before it
// builds the command, and a call on it reaches the Team.
func TestRunServesTheTeamSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	var gotSocket string
	var perm os.FileMode
	var gotReply mcpbridge.TeamReply
	var gotErr error
	backend := Backend{
		Name:          "fake-tk",
		SelfAssembles: true,
		TeamBridge:    true,
		Translate:     func([]byte) []Event { return nil },
		Command: func(d Dispatch) (*exec.Cmd, error) {
			gotSocket = d.TeamSocket
			if d.TeamSocket != "" {
				if fi, err := os.Stat(d.TeamSocket); err == nil {
					perm = fi.Mode().Perm()
				}
				gotReply, gotErr = dialTeam(d.TeamSocket, mcpbridge.TeamRequest{Tool: "talkoot_roster", Input: json.RawMessage(`{}`)})
			}
			return exec.Command("true"), nil
		},
	}
	var sawTool string
	a := teamAgent(t, "tk-run-1")
	r := NewRunner(a, backend, loadedRepo(t), nil).WithTeam(func(_ context.Context, tool string, _ json.RawMessage) mcpbridge.TeamReply {
		sawTool = tool
		return mcpbridge.TeamReply{Text: "- jev (you)"}
	})
	if err := r.Run(context.Background(), nopSink{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotSocket != teamSocketPath(a.InboxPath) {
		t.Fatalf("TeamSocket = %q, want %q", gotSocket, teamSocketPath(a.InboxPath))
	}
	if perm&0o077 != 0 {
		t.Errorf("socket perms = %o, want no group or world access", perm)
	}
	if gotErr != nil || gotReply.Text != "- jev (you)" || sawTool != "talkoot_roster" {
		t.Errorf("call: reply %+v, err %v, team saw %q", gotReply, gotErr, sawTool)
	}
}

// Without a Team, or on a backend without TeamBridge, the runner opens no
// team socket.
func TestRunOpensNoTeamSocketWithoutBoth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	team := func(context.Context, string, json.RawMessage) mcpbridge.TeamReply { return mcpbridge.TeamReply{} }
	for _, c := range []struct {
		name   string
		bridge bool
		team   Team
	}{{"no team", true, nil}, {"no bridge", false, team}} {
		var got string
		backend := Backend{
			Name: "fake-tk", SelfAssembles: true, TeamBridge: c.bridge,
			Translate: func([]byte) []Event { return nil },
			Command: func(d Dispatch) (*exec.Cmd, error) {
				got = d.TeamSocket
				return exec.Command("true"), nil
			},
		}
		if err := NewRunner(teamAgent(t, "tk-none"), backend, loadedRepo(t), nil).WithTeam(c.team).Run(context.Background(), nopSink{}); err != nil {
			t.Fatalf("%s: run: %v", c.name, err)
		}
		if got != "" {
			t.Errorf("%s: TeamSocket = %q, want none", c.name, got)
		}
	}
}

// A worker that reports its bridge did not connect is stopped, and Run says
// why. One that reports it connected runs on.
func TestRunStopsAWorkerWhoseBridgeDidNotLoad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	team := func(context.Context, string, json.RawMessage) mcpbridge.TeamReply { return mcpbridge.TeamReply{} }
	for _, c := range []struct {
		name, status, want string
	}{
		{"connected", "connected", ""},
		{"failed", "failed", "did not load in the worker (status failed)"},
		{"absent", "", "(status absent)"},
	} {
		backend := Backend{
			Name: "fake-tk", SelfAssembles: true, TeamBridge: true,
			Translate: func(line []byte) []Event {
				if string(line) != "ready" {
					return nil
				}
				servers := map[string]any{mcpbridge.ServerName: "connected"}
				if c.status != "" {
					servers[mcpbridge.TeamServerName] = c.status
				}
				return []Event{{Type: "agent_ready", Data: map[string]any{"mcp_servers": servers}}}
			},
			Command: func(Dispatch) (*exec.Cmd, error) {
				// A connected worker exits on its own. A stopped one would
				// otherwise sleep past the test's deadline.
				if c.want == "" {
					return exec.Command("sh", "-c", "echo ready"), nil
				}
				return exec.Command("sh", "-c", "echo ready; exec sleep 30"), nil
			},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := NewRunner(teamAgent(t, "tk-init"), backend, loadedRepo(t), nil).WithTeam(team).Run(ctx, nopSink{})
		late := ctx.Err()
		cancel()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: run: %v, want a clean exit", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: run: %v, want %q", c.name, err, c.want)
		case c.want != "" && late != nil:
			t.Errorf("%s: the runner waited for the deadline instead of stopping the worker", c.name)
		}
	}
}

// A worker that reports no MCP servers at all is not known to hold its seat
// tools, so it fails too.
func TestTeamBridgeLoadedNeedsAReport(t *testing.T) {
	if err := teamBridgeLoaded(map[string]any{"backend": "claude"}); err == nil || !strings.Contains(err.Error(), "did not report") {
		t.Errorf("no report: %v", err)
	}
	if err := teamBridgeLoaded(map[string]any{"mcp_servers": map[string]any{mcpbridge.TeamServerName: "connected"}}); err != nil {
		t.Errorf("connected: %v", err)
	}
	if err := teamBridgeLoaded(map[string]any{"mcp_servers": map[string]any{mcpbridge.TeamServerName: "pending"}}); err == nil {
		t.Error("a pending bridge counts as loaded")
	}
}

// A malformed call gets an error reply, never a dropped connection.
func TestHandleTeamConnAnswersAMalformedCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	r := (&Runner{agent: &swarm.Agent{ID: "w-1"}}).WithTeam(func(context.Context, string, json.RawMessage) mcpbridge.TeamReply {
		t.Error("a malformed call reached the team")
		return mcpbridge.TeamReply{}
	})
	sock := filepath.Join(shortSocketDir(t), "a.tk")
	sl, err := r.serveTeam(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("not json\n"))
	line, _ := bufio.NewReader(conn).ReadBytes('\n')
	var reply mcpbridge.TeamReply
	if err := json.Unmarshal(bytes.TrimSpace(line), &reply); err != nil || !reply.IsError {
		t.Errorf("reply = %q, want an error reply", line)
	}
}

// The claude worker's MCP config carries the Talkoot bridge beside the
// approval bridge, under --strict-mcp-config, so the seat tools are part of
// the surface terva chose.
func TestClaudeCommandWiresTheTalkootBridge(t *testing.T) {
	b := Compose(loadedRepo(t), demoTask(), Workspace{Path: "/w"})
	b.Policy = Policy{Posture: "workspace"}
	cmd, err := claudeCommand(Dispatch{Briefing: b, Dir: "/w", ApprovalSocket: "/lease/in.ap", TeamSocket: "/lease/in.tk", Tools: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := flagArg(cmd.Args, "--mcp-config")
	var parsed struct {
		MCPServers map[string]struct {
			Args    []string `json:"args"`
			Timeout int      `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("--mcp-config %q: %v", cfg, err)
	}
	if got := parsed.MCPServers[mcpbridge.TeamServerName].Args; strings.Join(got, " ") != "mcp-talkoot-bridge --socket /lease/in.tk" {
		t.Errorf("talkoot server args = %v", got)
	}
	// A question waits for a person, so the Talkoot server gets the approval
	// carrier's bound and not claude's own of about 28 hours.
	if got := parsed.MCPServers[mcpbridge.TeamServerName].Timeout; got != mcpbridge.TeamCallTimeoutMS {
		t.Errorf("talkoot server timeout = %d, want %d", got, mcpbridge.TeamCallTimeoutMS)
	}
	if got := parsed.MCPServers[mcpbridge.ServerName].Args; strings.Join(got, " ") != "mcp-approval-bridge --socket /lease/in.ap" {
		t.Errorf("approval server args = %v", got)
	}
	if n := strings.Count(strings.Join(cmd.Args, " "), "--strict-mcp-config"); n != 1 {
		t.Errorf("--strict-mcp-config appears %d times, want once: %v", n, cmd.Args)
	}
	// 🔑 The daemon gates each seat-tool call with the member's policy, so
	// claude pre-approves all four, in every posture, and asks nobody.
	allowed, _ := flagArg(cmd.Args, "--allowedTools")
	if allowed != "mcp__terva_talkoot__talkoot_send,mcp__terva_talkoot__talkoot_handoff,mcp__terva_talkoot__talkoot_roster,mcp__terva_talkoot__ask_user_question" {
		t.Errorf("--allowedTools %q, want the four seat tools", allowed)
	}
	// The bridge's question tool is the member's only one, so its answer
	// reaches the room.
	if disallowed, _ := flagArg(cmd.Args, "--disallowedTools"); disallowed != "AskUserQuestion" {
		t.Errorf("--disallowedTools %q, want claude's own question tool", disallowed)
	}

	// Without an approval socket, the Talkoot server still comes with
	// --strict-mcp-config, so no server of the person's can take its name.
	cmd, err = claudeCommand(Dispatch{Briefing: b, Dir: "/w", TeamSocket: "/lease/in.tk"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Errorf("a Talkoot server without the approval bridge must be strict: %v", cmd.Args)
	}

	// Without a team socket, the config holds the approval bridge alone and
	// nothing is pre-approved.
	cmd, err = claudeCommand(Dispatch{Briefing: b, Dir: "/w", ApprovalSocket: "/lease/in.ap"})
	if err != nil {
		t.Fatal(err)
	}
	if argv := strings.Join(cmd.Args, " "); strings.Contains(argv, mcpbridge.TeamServerName) || strings.Contains(argv, "--allowedTools") || strings.Contains(argv, "--disallowedTools") {
		t.Errorf("no team socket must mean no Talkoot bridge: %s", argv)
	}
}

// The init event's MCP servers reach agent_ready as a name-to-status map.
func TestClaudeInitReportsItsMCPServers(t *testing.T) {
	evs := translateClaude([]byte(`{"type":"system","subtype":"init","mcp_servers":[{"name":"terva_talkoot","status":"connected"},{"name":"terva_approvals","status":"failed"}]}`))
	if len(evs) != 1 || evs[0].Type != "agent_ready" {
		t.Fatalf("events = %+v", evs)
	}
	servers, _ := evs[0].Data["mcp_servers"].(map[string]any)
	if servers["terva_talkoot"] != "connected" || servers["terva_approvals"] != "failed" {
		t.Errorf("mcp_servers = %v", evs[0].Data["mcp_servers"])
	}
	if err := teamBridgeLoaded(evs[0].Data); err != nil {
		t.Errorf("the runner reads the translated init as: %v", err)
	}
	// An init with no list says so, rather than reading as an absent bridge.
	evs = translateClaude([]byte(`{"type":"system","subtype":"init"}`))
	if err := teamBridgeLoaded(evs[0].Data); err == nil || !strings.Contains(err.Error(), "did not report") {
		t.Errorf("an init with no list: %v", err)
	}
}

// A Talkoot member whose agent has no inbox path cannot derive its bridge
// socket, and the spawn fails rather than run without seat tools.
func TestRunRefusesATeamWithNoInbox(t *testing.T) {
	backend := Backend{
		Name: "fake-tk", SelfAssembles: true, TeamBridge: true,
		Translate: func([]byte) []Event { return nil },
		Command: func(Dispatch) (*exec.Cmd, error) {
			t.Error("the command was built for a member with no bridge socket")
			return exec.Command("true"), nil
		},
	}
	a := teamAgent(t, "tk-noinbox")
	a.InboxPath = ""
	team := func(context.Context, string, json.RawMessage) mcpbridge.TeamReply { return mcpbridge.TeamReply{} }
	err := NewRunner(a, backend, loadedRepo(t), nil).WithTeam(team).Run(context.Background(), nopSink{})
	if err == nil || !strings.Contains(err.Error(), "needs an inbox path") {
		t.Errorf("run: %v, want the refusal", err)
	}
}

func dialTeam(sock string, req mcpbridge.TeamRequest) (mcpbridge.TeamReply, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return mcpbridge.TeamReply{}, err
	}
	defer conn.Close()
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return mcpbridge.TeamReply{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if len(bytes.TrimSpace(line)) == 0 && err != nil {
		return mcpbridge.TeamReply{}, err
	}
	var reply mcpbridge.TeamReply
	err = json.Unmarshal(bytes.TrimSpace(line), &reply)
	return reply, err
}

// A call whose bridge hangs up ends its context, so a question that waits for
// a person closes its card instead of waiting for an answer no one reads.
func TestATeamCallEndsWhenTheBridgeHangsUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	ended := make(chan error, 1)
	r := (&Runner{agent: &swarm.Agent{ID: "w-1"}}).WithTeam(func(ctx context.Context, _ string, _ json.RawMessage) mcpbridge.TeamReply {
		<-ctx.Done()
		ended <- ctx.Err()
		return mcpbridge.TeamReply{}
	})
	sock := filepath.Join(shortSocketDir(t), "a.tk")
	sl, err := r.serveTeam(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte(`{"tool":"ask_user_question","input":{"question":"Still there?"}}` + "\n"))
	_ = conn.Close()
	select {
	case err := <-ended:
		if err != context.Canceled {
			t.Fatalf("the call ended with %v, want cancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call kept waiting after the bridge hung up")
	}
}

// Stray bytes from the bridge do not end a call. Only the hang-up does.
func TestATeamCallOutlivesStrayBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the team socket is a unix-domain socket")
	}
	ended := make(chan struct{})
	r := (&Runner{agent: &swarm.Agent{ID: "w-1"}}).WithTeam(func(ctx context.Context, _ string, _ json.RawMessage) mcpbridge.TeamReply {
		<-ctx.Done()
		close(ended)
		return mcpbridge.TeamReply{}
	})
	sock := filepath.Join(shortSocketDir(t), "a.tk")
	sl, err := r.serveTeam(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte(`{"tool":"ask_user_question","input":{"question":"Still there?"}}` + "\n"))
	// After the frame is read, so the stray byte reaches the watcher and not
	// the frame reader's buffer.
	time.Sleep(100 * time.Millisecond)
	_, _ = conn.Write([]byte("x"))
	select {
	case <-ended:
		t.Fatal("a stray byte ended the call")
	case <-time.After(300 * time.Millisecond):
	}
	_ = conn.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the call kept waiting after the bridge hung up")
	}
}
