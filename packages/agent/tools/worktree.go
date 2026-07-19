package tools

// The five worktree_* built-ins: thin arg/result glue over the
// packages/agent/worktree engine (stage 1 of the terva-git-worktree fold-in;
// the tool names, schemas, and descriptions are the extension's, so scripts
// and habits built against it keep working). All real logic — the
// available/claimed model, staleness, the registry, migration — lives in the
// engine package; these structs parse arguments, resolve the calling session,
// and marshal results.

import (
	"context"
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/agent/worktree"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// WorktreeCore is the shared engine handle the five tools embed. One instance
// per registry build, so every tool shares the Manager's mutex and sees one
// consistent view.
type WorktreeCore struct {
	Manager *worktree.Manager
	// Root / LegacyRoot / CWD mirror worktree.Env — fixed at registry build.
	Root       string
	LegacyRoot string
	CWD        string
}

// env assembles the per-call engine environment. The claim-owner session is
// resolved from the dispatch context at call time (the StatusTool pattern), so
// the tools stay correct when several live agents share one registry.
func (c *WorktreeCore) env(ctx context.Context, repoRoot string) worktree.Env {
	sess := ""
	if ag := core.AgentFromContext(ctx); ag != nil {
		sess, _ = ag.SessionIdentity()
	}
	return worktree.Env{Root: c.Root, LegacyRoot: c.LegacyRoot, CWD: c.CWD, SessionID: sess, RepoRoot: repoRoot}
}

// worktreeResult marshals an engine result as the tool's JSON payload; engine
// errors return as IsError text so the model can read the refusal (a claim
// conflict, a dirty-worktree refusal) and act on it.
func worktreeResult(v any, err error) (core.ToolResult, error) {
	if err != nil {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: err.Error()}},
			IsError: true,
		}, nil
	}
	b, merr := json.Marshal(v)
	if merr != nil {
		return core.ToolResult{}, fmt.Errorf("encode result: %w", merr)
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: string(b)}}}, nil
}

const worktreeRepoRootDesc = "optional path to the target git repo (absolute, or relative to cwd) to operate on instead of the cwd's repo; omit to use cwd. cwd_worktree still reflects your real cwd, not this override"

func worktreeRepoRootProp() map[string]any {
	return map[string]any{"type": "string", "description": worktreeRepoRootDesc}
}

func mustSchema(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// --- worktree_list ----------------------------------------------------------

type WorktreeListTool struct{ *WorktreeCore }

type worktreeListArgs struct {
	Match *struct {
		Status  string `json:"status"`
		BaseRef string `json:"base_ref"`
		Mine    bool   `json:"mine"`
	} `json:"match"`
	RepoRoot string `json:"repo_root"`
}

func (t *WorktreeListTool) Name() string { return "worktree_list" }
func (t *WorktreeListTool) Description() string {
	return "List git worktrees for the current repo. Read-only. Returns JSON: each worktree's name, path, branch, base_commit/base_ref, head_commit, status (available|claimed), claimed_by (self|<session>|null), stale_reason, dirty, and unmanaged; plus repo_key and cwd_worktree (the one you're in, or null). Optional `match` filters the results by {status, base_ref, mine} (e.g. available worktrees branched from main). Call this before worktree_create to reuse an existing worktree."
}
func (t *WorktreeListTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeListTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"match": map[string]any{
				"type":        "object",
				"description": "optional filter for the returned worktrees (does not affect cwd_worktree)",
				"properties": map[string]any{
					"status":   map[string]any{"type": "string", "enum": []string{"available", "claimed"}, "description": "only worktrees with this status"},
					"base_ref": map[string]any{"type": "string", "description": "only worktrees branched from this ref"},
					"mine":     map[string]any{"type": "boolean", "description": "only worktrees claimed by this session"},
				},
			},
			"repo_root": worktreeRepoRootProp(),
		},
	})
}

func (t *WorktreeListTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in worktreeListArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in) // args are optional
	}
	var filter worktree.ListFilter
	if in.Match != nil {
		filter = worktree.ListFilter{Status: in.Match.Status, BaseRef: in.Match.BaseRef, Mine: in.Match.Mine}
	}
	return worktreeResult(t.Manager.List(t.env(ctx, in.RepoRoot), filter))
}

// --- worktree_create --------------------------------------------------------

type WorktreeCreateTool struct{ *WorktreeCore }

type worktreeCreateArgs struct {
	Name             string `json:"name"`
	Base             string `json:"base"`
	ReuseIfAvailable *bool  `json:"reuse_if_available"`
	RepoRoot         string `json:"repo_root"`
}

func (t *WorktreeCreateTool) Name() string { return "worktree_create" }
func (t *WorktreeCreateTool) Description() string {
	return "Create (or reuse) a git worktree and claim it for this session. Provide `name` (slugged; becomes branch wt/<name>). Optional `base` (ref/SHA to branch from; default current HEAD) and `reuse_if_available` (default true: if <name> exists and is available, claim and return it instead of erroring). Returns the worktree JSON incl. `reused`. Errors if <name> is claimed by another live session."
}
func (t *WorktreeCreateTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeCreateTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":               map[string]any{"type": "string", "description": "worktree name; slugged into branch wt/<name>"},
			"base":               map[string]any{"type": "string", "description": "ref or SHA to branch from (default: current HEAD)"},
			"reuse_if_available": map[string]any{"type": "boolean", "description": "if <name> exists and is available, claim and return it (default true)"},
			"repo_root":          worktreeRepoRootProp(),
		},
		"required": []string{"name"},
	})
}

func (t *WorktreeCreateTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in worktreeCreateArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	reuse := true // default true per the design
	if in.ReuseIfAvailable != nil {
		reuse = *in.ReuseIfAvailable
	}
	return worktreeResult(t.Manager.Create(t.env(ctx, in.RepoRoot), worktree.CreateArgs{
		Name: in.Name, Base: in.Base, ReuseIfAvailable: reuse,
	}))
}

// --- worktree_claim / worktree_release --------------------------------------

type worktreeNameArgs struct {
	Name     string `json:"name"`
	RepoRoot string `json:"repo_root"`
}

func worktreeNameSchema(desc string) json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":      map[string]any{"type": "string", "description": desc},
			"repo_root": worktreeRepoRootProp(),
		},
		"required": []string{"name"},
	})
}

type WorktreeClaimTool struct{ *WorktreeCore }

func (t *WorktreeClaimTool) Name() string { return "worktree_claim" }
func (t *WorktreeClaimTool) Description() string {
	return "Claim an existing available worktree for this session by `name`, without creating one — use it to take over an idle worktree another agent left (see worktree_list). Idempotent if you already hold it; errors if it is claimed by another live session. Returns the worktree JSON."
}
func (t *WorktreeClaimTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeClaimTool) Schema() json.RawMessage {
	return worktreeNameSchema("name of an existing worktree to claim")
}

func (t *WorktreeClaimTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in worktreeNameArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	return worktreeResult(t.Manager.Claim(t.env(ctx, in.RepoRoot), worktree.ClaimArgs{Name: in.Name}))
}

type WorktreeReleaseTool struct{ *WorktreeCore }

func (t *WorktreeReleaseTool) Name() string { return "worktree_release" }
func (t *WorktreeReleaseTool) Description() string {
	return "Release this session's claim on a worktree by `name` so another agent can take it, without removing the worktree. Clears a stale claim too; errors only if the worktree is held by another live session. Returns JSON { name, released, status }."
}
func (t *WorktreeReleaseTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeReleaseTool) Schema() json.RawMessage {
	return worktreeNameSchema("name of the worktree to release")
}

func (t *WorktreeReleaseTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in worktreeNameArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	return worktreeResult(t.Manager.Release(t.env(ctx, in.RepoRoot), worktree.ReleaseArgs{Name: in.Name}))
}

// --- worktree_remove --------------------------------------------------------

type WorktreeRemoveTool struct{ *WorktreeCore }

type worktreeRemoveArgs struct {
	Name         string `json:"name"`
	Force        bool   `json:"force"`
	DeleteBranch bool   `json:"delete_branch"`
	RepoRoot     string `json:"repo_root"`
}

func (t *WorktreeRemoveTool) Name() string { return "worktree_remove" }
func (t *WorktreeRemoveTool) Description() string {
	return "Remove a managed git worktree by `name`. Refuses if it has uncommitted changes or unmerged/unpushed commits unless `force` is true. Leaves the branch by default; set `delete_branch` to also delete wt/<name>. Returns JSON { name, removed, branch_deleted }."
}
func (t *WorktreeRemoveTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeRemoveTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":          map[string]any{"type": "string", "description": "name of the worktree to remove"},
			"force":         map[string]any{"type": "boolean", "description": "remove even with uncommitted or unmerged/unpushed work"},
			"delete_branch": map[string]any{"type": "boolean", "description": "also delete the wt/<name> branch (default false)"},
			"repo_root":     worktreeRepoRootProp(),
		},
		"required": []string{"name"},
	})
}

func (t *WorktreeRemoveTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in worktreeRemoveArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	return worktreeResult(t.Manager.Remove(t.env(ctx, in.RepoRoot), worktree.RemoveArgs{
		Name: in.Name, Force: in.Force, DeleteBranch: in.DeleteBranch,
	}))
}
