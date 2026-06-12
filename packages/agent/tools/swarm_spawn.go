package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// SwarmSpawnTool lets the main agent fork a background sub-agent
// against the host's cwd via swarm.Swarm.SpawnReq. The sub-agent runs
// in parallel: the tool returns the agent id immediately and the main
// turn continues uninterrupted. The user can monitor / chat with the
// spawned agent via /swarm.
//
// Gated by the auto_swarm_enabled config flag at call time so a user
// can flip it off mid-session and the next call refuses cleanly
// without re-registering the tool.
type SwarmSpawnTool struct {
	// Swarm is the supervisor used to spawn agents. Nil means
	// "auto-swarm not available in this mode" and the tool always
	// errors.
	Swarm *swarm.Swarm

	// Enabled reads the live config flag. Lets users toggle from
	// /settings without rebuilding the agent. When nil, the tool
	// is treated as disabled.
	Enabled func() bool

	// OnSpawned, if set, is called after every successful spawn with
	// the new agent + the task it was started with. Used by the
	// interactive host to track agents and surface a summary back
	// in the main chat once they all finish.
	OnSpawned func(agent *swarm.Agent, task string)
}

type swarmSpawnArgs struct {
	Task     string `json:"task"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

const swarmSpawnSchema = `{
  "type": "object",
  "properties": {
    "task": {
      "type": "string",
      "description": "The full task description for the sub-agent. Be specific: the sub-agent has the same tools (read/write/edit/bash) and shares this working directory, but starts with NO context from this conversation."
    },
    "model": {
      "type": "string",
      "description": "Optional model id to pin the sub-agent to (e.g. \"claude-sonnet-4-5\", \"gpt-5\"). Defaults to the host's current model."
    },
    "provider": {
      "type": "string",
      "description": "Optional provider id (e.g. \"anthropic\", \"openai\"). Usually paired with model."
    }
  },
  "required": ["task"]
}`

func (t *SwarmSpawnTool) Name() string { return "swarm_spawn" }
func (t *SwarmSpawnTool) Description() string {
	return "Spawn a background sub-agent to work on a parallel sub-task. Returns the sub-agent id immediately; the sub-agent keeps running while this conversation continues. Useful for splitting independent work (write tests while implementing a feature, refactor module A while drafting module B). The sub-agent shares this working directory and has the same tools."
}
func (t *SwarmSpawnTool) Schema() json.RawMessage { return json.RawMessage(swarmSpawnSchema) }

func (t *SwarmSpawnTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	if t.Swarm == nil {
		return toolErr("swarm_spawn: swarm supervisor not available in this mode"), nil
	}
	if t.Enabled == nil || !t.Enabled() {
		return toolErr("swarm_spawn: auto-swarm is disabled. Ask the user to enable it from /settings before delegating sub-tasks."), nil
	}
	var a swarmSpawnArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	task := strings.TrimSpace(a.Task)
	if task == "" {
		return toolErr("swarm_spawn: task is required"), nil
	}

	agent, err := t.Swarm.SpawnReq(ctx, swarm.SpawnRequest{
		Task:     task,
		Model:    strings.TrimSpace(a.Model),
		Provider: strings.TrimSpace(a.Provider),
	})
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("swarm_spawn: %w", err)
	}
	if t.OnSpawned != nil {
		t.OnSpawned(agent, task)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "spawned sub-agent %s\n", agent.ID)
	fmt.Fprintf(&sb, "task: %s\n", truncateTask(task, 200))
	if a.Model != "" {
		fmt.Fprintf(&sb, "model: %s\n", a.Model)
	}
	if a.Provider != "" {
		fmt.Fprintf(&sb, "provider: %s\n", a.Provider)
	}
	sb.WriteString("\nThe sub-agent is running in the background. Use /swarm in the TUI to monitor it. ")
	sb.WriteString("This conversation continues immediately; do not wait for the sub-agent to finish before working on the next thing.")
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: sb.String()}},
		Details: map[string]any{
			"agent_id": agent.ID,
			"task":     task,
			"model":    a.Model,
			"provider": a.Provider,
		},
	}, nil
}

func toolErr(msg string) core.ToolResult {
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: msg}},
		IsError: true,
	}
}

func truncateTask(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
