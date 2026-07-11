package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

var (
	stubOnce sync.Once
	stubPath string
	stubErr  error
)

func buildStub(t *testing.T) string {
	t.Helper()
	stubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mcpstub")
		if err != nil {
			stubErr = err
			return
		}
		out := filepath.Join(dir, "mcpstub")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		if b, err := exec.Command("go", "build", "-o", out, "./testdata/cmd/mcpstub").CombinedOutput(); err != nil {
			stubErr = err
			_ = b
			return
		}
		stubPath = out
	})
	if stubErr != nil {
		t.Fatalf("build mcpstub: %v", stubErr)
	}
	return stubPath
}

func startStub(t *testing.T, args ...string) *Client {
	t.Helper()
	cl, err := Start(context.Background(), "stub", ServerConfig{Command: buildStub(t), Args: args}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(cl.Stop)
	return cl
}

func TestHandshakeAndToolList(t *testing.T) {
	cl := startStub(t)
	if len(cl.Tools()) != 4 {
		t.Fatalf("tools = %d, want 4", len(cl.Tools()))
	}
	if cl.Tools()[0].Name != "add" {
		t.Errorf("first tool = %q", cl.Tools()[0].Name)
	}
}

func TestToolListPagination(t *testing.T) {
	cl := startStub(t, "--paginate")
	if len(cl.Tools()) != 4 {
		t.Fatalf("paginated tools = %d, want 4 (cursor loop broken)", len(cl.Tools()))
	}
}

func TestCallToolRoundTrip(t *testing.T) {
	cl := startStub(t)
	res, err := cl.CallTool(context.Background(), "add", json.RawMessage(`{"a":2,"b":40}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "42" {
		t.Fatalf("result = %+v", res)
	}
}

func TestCallToolErrorResult(t *testing.T) {
	cl := startStub(t)
	res, err := cl.CallTool(context.Background(), "fail", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("want isError result")
	}
}

func TestCallToolUnknownIsRPCError(t *testing.T) {
	cl := startStub(t)
	if _, err := cl.CallTool(context.Background(), "nope", nil); err == nil {
		t.Fatal("want rpc error for unknown tool")
	}
}

func TestServerDeathFailsPendingFast(t *testing.T) {
	// A server that dies must fail callers promptly via the read-loop
	// EOF path, not strand them until the 60s call timeout.
	cl, err := Start(context.Background(), "stub", ServerConfig{Command: buildStub(t), Args: []string{"--die-after-init"}}, "", nil)
	if err != nil {
		// Dying right after init may already surface at Start (the
		// tools/list hits EOF) — that is the same guarantee.
		return
	}
	defer cl.Stop()
	done := make(chan error, 1)
	go func() {
		_, cerr := cl.CallTool(context.Background(), "add", nil)
		done <- cerr
	}()
	select {
	case cerr := <-done:
		if cerr == nil {
			t.Fatal("call against dead server should error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pending call hung after server death")
	}
}

func TestManagerNamespacingAndRouting(t *testing.T) {
	stub := buildStub(t)
	cfg := &Config{Servers: map[string]ServerConfig{
		"alpha": {Command: stub},
		"beta":  {Command: stub},
	}}
	m := StartAll(context.Background(), cfg, "", nil)
	defer m.StopAll()
	if w := m.Warnings(); len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	tools := m.Tools()
	if len(tools) != 8 {
		t.Fatalf("tools = %d, want 8 (4 per server)", len(tools))
	}
	names := map[string]bool{}
	for _, ti := range tools {
		names[ti.Name] = true
	}
	for _, want := range []string{"mcp_alpha_add", "mcp_beta_add", "mcp_alpha_imagetool"} {
		if !names[want] {
			t.Errorf("missing namespaced tool %s in %v", want, names)
		}
	}
	res, err := m.Call(context.Background(), "mcp_beta_add", json.RawMessage(`{"a":1,"b":2}`))
	if err != nil || res.Content[0].Text != "3" {
		t.Fatalf("routed call = %+v, %v", res, err)
	}
}

func TestManagerBrokenServerIsWarningNotFatal(t *testing.T) {
	cfg := &Config{Servers: map[string]ServerConfig{
		"good": {Command: buildStub(t)},
		"bad":  {Command: filepath.Join(testsupport.TempDir(t), "does-not-exist")},
	}}
	m := StartAll(context.Background(), cfg, "", nil)
	defer m.StopAll()
	if len(m.Warnings()) != 1 || !strings.Contains(m.Warnings()[0], "bad") {
		t.Fatalf("warnings = %v", m.Warnings())
	}
	if len(m.Tools()) != 4 {
		t.Fatalf("good server tools = %d, want 4", len(m.Tools()))
	}
}

func TestToolAdapterConvertsContent(t *testing.T) {
	stub := buildStub(t)
	m := StartAll(context.Background(), &Config{Servers: map[string]ServerConfig{"s": {Command: stub}}}, "", nil)
	defer m.StopAll()

	var img *Tool
	for _, ti := range m.Tools() {
		if ti.Name == "mcp_s_imagetool" {
			img = m.NewTool(ti)
		}
	}
	if img == nil {
		t.Fatal("imagetool not found")
	}
	res, err := img.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ib, ok := res.Content[0].(provider.ImageBlock)
	if !ok {
		t.Fatalf("want ImageBlock, got %T", res.Content[0])
	}
	if ib.MimeType != "image/png" || len(ib.Data) == 0 {
		t.Errorf("image block = %+v", ib)
	}
}

func TestReadOnlyHintMapsToToolInfo(t *testing.T) {
	stub := buildStub(t)
	m := StartAll(context.Background(), &Config{Servers: map[string]ServerConfig{"s": {Command: stub}}}, "", nil)
	defer m.StopAll()
	got := map[string]bool{}
	for _, ti := range m.Tools() {
		got[ti.Name] = ti.ReadOnly
	}
	if !got["mcp_s_peek"] {
		t.Error("readOnlyHint-annotated tool should report ReadOnly")
	}
	if got["mcp_s_add"] {
		t.Error("unannotated tool must default to mutating")
	}
}

func TestNamespaceToolSanitizes(t *testing.T) {
	if got := NamespaceTool("my server", "weird/tool"); got != "mcp_my_server_weird_tool" {
		t.Errorf("NamespaceTool = %q", got)
	}
}

func TestConfigEnvCannotReintroduceInjection(t *testing.T) {
	// Belt-and-braces check at the spawn boundary: a config env of
	// LD_PRELOAD must not reach the child even if load-time
	// validation is bypassed.
	stub := buildStub(t)
	cl, err := Start(context.Background(), "s", ServerConfig{
		Command: stub,
		Env:     map[string]string{"LD_PRELOAD": "/tmp/evil.so", "PATH": "/tmp", "MY_TOKEN": "ok"},
	}, "", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cl.Stop()
	for _, kv := range cl.cmd.Env {
		if strings.HasPrefix(kv, "LD_PRELOAD=") {
			t.Error("LD_PRELOAD leaked into MCP server env")
		}
		if kv == "PATH=/tmp" {
			t.Error("PATH override leaked into MCP server env")
		}
	}
	found := false
	for _, kv := range cl.cmd.Env {
		if kv == "MY_TOKEN=ok" {
			found = true
		}
	}
	if !found {
		t.Error("benign config env var was dropped")
	}
}

func TestStartRunsServerInProjectCWD(t *testing.T) {
	// A server spawned with an explicit cwd must run there, so its
	// relative-path / directory-listing tools resolve against the user's
	// project rather than terva's launch directory.
	cwd := testsupport.TempDir(t)
	cl, err := Start(context.Background(), "stub", ServerConfig{Command: buildStub(t)}, cwd, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cl.Stop()
	if cl.cmd.Dir != cwd {
		t.Fatalf("server cwd = %q, want %q", cl.cmd.Dir, cwd)
	}
}

func TestManagerStartOneInheritsProjectCWD(t *testing.T) {
	// A server started mid-session via StartOne must inherit the project
	// cwd captured at StartAll, not terva's launch directory.
	cwd := testsupport.TempDir(t)
	m := StartAll(context.Background(), nil, cwd, nil) // empty; captures cwd
	if err := m.StartOne(context.Background(), "alpha", ServerConfig{Command: buildStub(t)}, nil); err != nil {
		t.Fatalf("StartOne: %v", err)
	}
	m.mu.Lock()
	cl := m.clients["alpha"]
	m.mu.Unlock()
	if cl == nil {
		t.Fatal("server not started")
	}
	defer cl.Stop()
	if cl.cmd.Dir != cwd {
		t.Fatalf("StartOne server cwd = %q, want %q", cl.cmd.Dir, cwd)
	}
}
