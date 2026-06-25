package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestStartOneStopOneLifecycle proves the live per-server lifecycle the
// /mcp dialog rides: an empty Manager grows a server (Status/Tools/routes
// appear) and shrinks it again (everything for that server vanishes).
func TestStartOneStopOneLifecycle(t *testing.T) {
	stub := buildStub(t)
	m := StartAll(context.Background(), nil, nil) // empty, no servers
	defer m.StopAll()
	if len(m.Tools()) != 0 || len(m.Status()) != 0 {
		t.Fatalf("empty manager: %d tools, %d status; want 0/0", len(m.Tools()), len(m.Status()))
	}

	if err := m.StartOne(context.Background(), "alpha", ServerConfig{Command: stub}, nil); err != nil {
		t.Fatalf("StartOne: %v", err)
	}
	st := m.Status()
	if len(st) != 1 || !st[0].Connected || st[0].Name != "alpha" || st[0].ToolCount != 4 {
		t.Fatalf("status after StartOne = %+v", st)
	}
	if len(m.Tools()) != 4 {
		t.Fatalf("tools after StartOne = %d, want 4", len(m.Tools()))
	}
	if _, err := m.Call(context.Background(), "mcp_alpha_add", json.RawMessage(`{"a":1,"b":2}`)); err != nil {
		t.Fatalf("routed call after StartOne: %v", err)
	}

	m.StopOne("alpha")
	if len(m.Status()) != 0 || len(m.Tools()) != 0 {
		t.Fatalf("after StopOne: %d status, %d tools; want 0/0", len(m.Status()), len(m.Tools()))
	}
	if _, err := m.Call(context.Background(), "mcp_alpha_add", json.RawMessage(`{}`)); err == nil {
		t.Fatal("call after StopOne should fail — its route is gone")
	}
}

// TestStartOneIdempotent: a second StartOne for a running server is a
// no-op, never a duplicate client/tool set.
func TestStartOneIdempotent(t *testing.T) {
	stub := buildStub(t)
	m := StartAll(context.Background(), nil, nil)
	defer m.StopAll()
	if err := m.StartOne(context.Background(), "alpha", ServerConfig{Command: stub}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.StartOne(context.Background(), "alpha", ServerConfig{Command: stub}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.Tools()) != 4 || len(m.Status()) != 1 {
		t.Fatalf("idempotent StartOne changed counts: %d tools, %d status", len(m.Tools()), len(m.Status()))
	}
}

// TestStartOneFailedIsWarningAndError: a bad command records a warning AND
// returns the error (so the dialog can show "failed"), and leaves the
// Manager usable for the next server.
func TestStartOneFailedIsWarningAndError(t *testing.T) {
	m := StartAll(context.Background(), nil, nil)
	defer m.StopAll()
	err := m.StartOne(context.Background(), "bad", ServerConfig{Command: filepath.Join(testsupport.TempDir(t), "nope")}, nil)
	if err == nil {
		t.Fatal("StartOne on a missing command should return an error")
	}
	if len(m.Status()) != 0 {
		t.Fatalf("failed StartOne left a live server: %+v", m.Status())
	}
	if len(m.Warnings()) != 1 || !strings.Contains(m.Warnings()[0], "bad") {
		t.Fatalf("warnings after failed StartOne = %v", m.Warnings())
	}
	if err := m.StartOne(context.Background(), "good", ServerConfig{Command: buildStub(t)}, nil); err != nil {
		t.Fatalf("manager unusable after a failed StartOne: %v", err)
	}
	if len(m.Tools()) != 4 {
		t.Fatalf("good server after a bad one = %d tools, want 4", len(m.Tools()))
	}
}

// TestStartOneFailureWarningsDoNotAccumulate: repeated failures for the
// same server keep exactly one warning (clearServerWarnsLocked), so the
// dialog never shows a growing pile of stale "failed" notes.
func TestStartOneFailureWarningsDoNotAccumulate(t *testing.T) {
	m := StartAll(context.Background(), nil, nil)
	defer m.StopAll()
	bad := ServerConfig{Command: filepath.Join(testsupport.TempDir(t), "nope")}
	_ = m.StartOne(context.Background(), "bad", bad, nil)
	_ = m.StartOne(context.Background(), "bad", bad, nil)
	if len(m.Warnings()) != 1 {
		t.Fatalf("repeated failed StartOne piled warnings: %v", m.Warnings())
	}
}
