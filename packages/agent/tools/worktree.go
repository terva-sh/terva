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
	"terva.sh/terva/packages/i18n"
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

const worktreeRepoRootDesc = "An optional path to the git repository to operate on. Give an absolute path, or a path relative to the working directory. Omit this field to use the repository of the working directory. The value of cwd_worktree always shows your true working directory, and not this path."

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
	return i18n.D("tool.worktree_list.description", "List the git worktrees of the current repository. This tool only reads, and it changes nothing. The tool returns JSON. For each worktree it gives the name, the path, the branch, base_commit, base_ref, head_commit, the status, claimed_by, stale_reason, dirty, and unmanaged. The status is available or claimed. The value of claimed_by is self, a session id, or null. The JSON also gives repo_key and cwd_worktree, which is the worktree that you are in, or null.\n\nThe optional field `match` selects the results by status, base_ref, and mine. For example, it can select the available worktrees with a branch from main. Call this tool before worktree_create, so that you can use a worktree that exists.")
}
func (t *WorktreeListTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeListTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"match": map[string]any{
				"type":        "object",
				"description": "An optional filter for the worktrees that the tool returns. The filter does not change cwd_worktree.",
				"properties": map[string]any{
					"status":   map[string]any{"type": "string", "enum": []string{"available", "claimed"}, "description": "Return the worktrees with this status only."},
					"base_ref": map[string]any{"type": "string", "description": "Return the worktrees with a branch from this ref only."},
					"mine":     map[string]any{"type": "boolean", "description": "Return the worktrees that this session claimed only."},
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
	return i18n.D("tool.worktree_create.description", "Make a git worktree, or use one that exists, and claim it for this session. Give `name`. The tool makes a slug from the name, and the branch becomes wt/<name>.\n\nThe optional field `base` gives the ref or the SHA for the branch, and the default is the current HEAD. The optional field `reuse_if_available` has the default true. With this default, if <name> exists and is available, the tool claims it and returns it, and does not report an error.\n\nThe tool returns the worktree JSON, which includes `reused`. The tool reports an error if another live session claimed <name>.")
}
func (t *WorktreeCreateTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeCreateTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":               map[string]any{"type": "string", "description": "The name of the worktree. The tool makes a slug from it for the branch wt/<name>."},
			"base":               map[string]any{"type": "string", "description": "The ref or the SHA for the branch. The default is the current HEAD."},
			"reuse_if_available": map[string]any{"type": "boolean", "description": "If <name> exists and is available, claim it and return it. The default is true."},
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
	return i18n.D("tool.worktree_claim.description", "Claim an available worktree for this session by `name`, and do not make a new worktree. Use this tool to take a worktree that another agent left, as worktree_list shows. If you already hold the worktree, the tool changes nothing. The tool reports an error if another live session claimed the worktree. The tool returns the worktree JSON.")
}
func (t *WorktreeClaimTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeClaimTool) Schema() json.RawMessage {
	return worktreeNameSchema("The name of a worktree that exists, for the tool to claim.")
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
	return i18n.D("tool.worktree_release.description", "Release the claim of this session on a worktree by `name`, so that another agent can take it. The tool does not remove the worktree. The tool also releases a claim that is stale. The tool reports an error only if another live session holds the worktree. The tool returns JSON with name, released, and status.")
}
func (t *WorktreeReleaseTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeReleaseTool) Schema() json.RawMessage {
	return worktreeNameSchema("The name of the worktree to release.")
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
	return i18n.D("tool.worktree_remove.description", "Remove a managed git worktree by `name`. The tool refuses if the worktree has changes that you did not commit, or commits that you did not merge or push. To remove the worktree in these conditions, set `force` to true. The tool keeps the branch by default. To also delete the branch wt/<name>, set `delete_branch`. The tool returns JSON with name, removed, and branch_deleted.")
}
func (t *WorktreeRemoveTool) ToolGroupName() string { return "worktree" }
func (t *WorktreeRemoveTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":          map[string]any{"type": "string", "description": "The name of the worktree to remove."},
			"force":         map[string]any{"type": "boolean", "description": "Remove the worktree also when it holds work that you did not commit, merge, or push."},
			"delete_branch": map[string]any{"type": "boolean", "description": "Also delete the branch wt/<name>. The default is false."},
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
