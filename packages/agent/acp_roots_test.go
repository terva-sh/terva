//go:build terva_acp

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestACPPeerDirectoriesNewLoadAndClear(t *testing.T) {
	ctx := context.Background()
	primary, peer, outside := testsupport.TempDir(t), testsupport.TempDir(t), testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "fixture-key")
	f := &acpFactory{ctx: ctx, args: build.Args{Provider: "openai", Model: "gpt-5", CWD: primary, NoExt: true, NoMCP: true}, version: "test"}
	sa, err := f.NewSessionAgent(ctx, primary, []string{peer}, nil, allowingConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	if !sa.Sandbox.Locked() {
		t.Fatal("ACP jail default changed")
	}
	tool, ok := sa.Agent.LookupTool("write")
	if !ok {
		t.Fatal("missing write tool")
	}
	w := tool.(*tools.WriteTool)
	if w.Sandbox != sa.Sandbox {
		t.Fatal("tool lost shared sandbox")
	}
	if err := w.Sandbox.CheckPath(filepath.Join(peer, "file")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(peer, "file")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "fixture"})
	allowed, reason, _ := sa.Gate.Check(ctx, "write", args, "fixture", "peer-write")
	if !allowed {
		t.Fatal("workspace gate refused peer write", reason)
	}
	sa.Gate.SetMode(permission.ApprovalPlan)
	if allowed, _, _ := sa.Gate.Check(ctx, "write", args, "fixture", "peer-plan-write"); allowed {
		t.Fatal("peer grant bypassed the plan gate")
	}
	sa.Gate.SetMode(permission.ApprovalWorkspace)
	result, err := w.Execute(ctx, args, nil)
	if err != nil || result.IsError {
		t.Fatalf("peer write: %+v, %v", result, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "fixture" {
		t.Fatal("peer tool write did not reach disk")
	}
	if err := w.Sandbox.CheckPath(filepath.Join(outside, "file")); err == nil {
		t.Fatal("ACP grant escaped peers")
	}
	if err := sa.Session.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	path := sa.Session.Path
	_ = sa.Session.Close()
	sa.Cleanup()
	list := f.ListSessions(primary)
	if len(list) != 1 {
		t.Fatal("ACP listing lost the session")
	}
	sa, _, err = f.LoadSessionAgent(ctx, path, primary, []string{peer}, nil, allowingConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sa.Sandbox.CheckPath(filepath.Join(peer, "file")); err != nil {
		t.Fatal("resume lost peer root", err)
	}
	if sa.TrustWorkspace == nil {
		t.Fatal("fixture has no trust rebuild callback")
	}
	if err := sa.TrustWorkspace(false); err != nil {
		t.Fatal(err)
	}
	tool, _ = sa.Agent.LookupTool("write")
	if err := tool.(*tools.WriteTool).Sandbox.CheckPath(filepath.Join(peer, "file")); err != nil {
		t.Fatal("tool rebuild lost root", err)
	}
	_ = sa.Session.Close()
	sa.Cleanup()
	sa, _, err = f.LoadSessionAgent(ctx, path, primary, []string{}, nil, allowingConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Cleanup()
	defer sa.Session.Close()
	if err := sa.Sandbox.CheckPath(filepath.Join(peer, "file")); err == nil {
		t.Fatal("explicit empty list did not revoke roots")
	}
	if _, err := f.NewSessionAgent(ctx, primary, []string{filepath.Join(peer, "missing")}, nil, allowingConfirmer{}); err == nil {
		t.Fatal("invalid ACP root accepted")
	}
	if _, err := f.NewSessionAgent(ctx, primary, []string{"."}, nil, allowingConfirmer{}); err == nil {
		t.Fatal("relative ACP root accepted")
	}
}

func TestACPPeerAuthorityRequiresCurrentClientGrant(t *testing.T) {
	ctx := context.Background()
	primary, peer, launch, forged := testsupport.TempDir(t), testsupport.TempDir(t), testsupport.TempDir(t), testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "fixture-key")
	f := &acpFactory{ctx: ctx, args: build.Args{Provider: "openai", Model: "gpt-5", CWD: primary, AddDirs: []string{launch}, NoExt: true, NoMCP: true}, version: "test"}
	sa, err := f.NewSessionAgent(ctx, primary, []string{peer, peer}, nil, allowingConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sa.Session.Meta.AdditionalRoots, []string{peer}) {
		t.Fatal("launch grants leaked into peer metadata", sa.Session.Meta.AdditionalRoots)
	}
	if err := sa.Sandbox.CheckPath(filepath.Join(launch, "file")); err != nil {
		t.Fatal("launch grant missing", err)
	}
	if err := sa.Session.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	path := sa.Session.Path
	if err := sa.Session.SetAdditionalRoots([]string{forged, filepath.Join(peer, "missing")}); err != nil {
		t.Fatal(err)
	}
	_ = sa.Session.Close()
	sa.Cleanup()
	listing, err := json.Marshal(f.ListSessions(primary))
	if err != nil {
		t.Fatal(err)
	}
	var listed []map[string]json.RawMessage
	if err := json.Unmarshal(listing, &listed); err != nil || len(listed) != 1 {
		t.Fatal("invalid ACP session listing", err)
	}
	if _, exists := listed[0]["additionalDirectories"]; exists {
		t.Fatal("ACP listing advertises transcript-sourced peers as load grants")
	}
	withoutLaunch := *f
	withoutLaunch.args.AddDirs = nil
	sa, _, err = withoutLaunch.LoadSessionAgent(ctx, path, primary, nil, nil, allowingConfirmer{})
	if err != nil {
		t.Fatal("historical invalid peer blocked transcript load", err)
	}
	for _, dir := range []string{peer, launch, forged} {
		if err := sa.Sandbox.CheckPath(filepath.Join(dir, "file")); err == nil {
			t.Fatal("load restored authority from another launch or transcript", dir)
		}
	}
	if len(sa.Session.Meta.AdditionalRoots) != 0 {
		t.Fatal("omitted input retained old peer metadata")
	}
	_ = sa.Session.Close()
	sa.Cleanup()
	sa, _, err = f.LoadSessionAgent(ctx, path, primary, []string{}, nil, allowingConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Cleanup()
	defer sa.Session.Close()
	if len(sa.Session.Meta.AdditionalRoots) != 0 || sa.Sandbox.CheckPath(filepath.Join(launch, "file")) != nil {
		t.Fatal("clear persisted launch grants or removed current launch authority")
	}
	if _, _, err := f.LoadSessionAgent(ctx, path, primary, []string{"."}, nil, allowingConfirmer{}); err == nil {
		t.Fatal("load accepted a relative ACP root")
	}
}
