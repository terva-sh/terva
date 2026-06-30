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

	// HostProvider / HostModel are the host agent's current provider and
	// model. They back the `tier` parameter: a weak/medium/strong tier
	// resolves to a concrete model of that strength for HostProvider,
	// capped so the sub-agent is never stronger than the host (see
	// ResolveSwarmTier). Empty disables tier resolution (the sub-agent
	// inherits the host model), so older construction sites still work.
	HostProvider string
	HostModel    string

	// Tiers is the user's per-provider tier→model override (Config.SwarmTiers).
	// It composes over the built-in family table, so a provider terva can't
	// guess (a gateway) still resolves a tier when the user configures it. Nil
	// is fine — resolution falls back to the built-in table then the host model.
	Tiers SwarmTierMap

	// PersonaResolver, when set, validates a model-supplied persona NAME
	// against the trusted library and returns its canonical name (error if
	// unknown). Nil skips the existence check; the inline path-rejection in
	// Execute still applies regardless, so the model can never name a path.
	PersonaResolver func(name string) (string, error)
}

type swarmSpawnArgs struct {
	Task     string `json:"task"`
	Persona  string `json:"persona,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Tier     string `json:"tier,omitempty"`
}

const swarmSpawnSchema = `{
  "type": "object",
  "properties": {
    "task": {
      "type": "string",
      "description": "The full task description for the sub-agent. Be specific: the sub-agent has the same tools (read/write/edit/bash) and shares this working directory, but starts with NO context from this conversation."
    },
    "persona": {
      "type": "string",
      "description": "Optional persona NAME to boot the sub-agent as a specialist (e.g. a security or test reviewer). Must be one of the dispatchable persona names listed in your instructions — a NAME, never a path. Pick the persona whose focus matches the sub-task; omit for a general-purpose sub-agent."
    },
    "tier": {
      "type": "string",
      "enum": ["weak", "medium", "strong"],
      "description": "Optional model strength for the sub-agent: weak (cheap/fast, e.g. Haiku), medium (e.g. Sonnet), strong (e.g. Opus). Resolved for the host provider and PINNED to it (the sub-agent runs on the host provider at this strength), never stronger than the host model. Use weak for routine sub-tasks to save cost. Only valid when model+provider are omitted; if the provider has no tier mapping the host model is used."
    },
    "model": {
      "type": "string",
      "description": "Optional model id to pin the sub-agent to. Normally OMIT both model and provider so the sub-agent inherits the host session's resolved provider/model/auth route. A model id is only valid for its provider, so if you set this you must also set provider. Do not infer the provider from the model name."
    },
    "provider": {
      "type": "string",
      "description": "Optional provider id. Normally OMIT both model and provider so the sub-agent inherits the host session. If you set this you must also set model."
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

	persona := strings.TrimSpace(a.Persona)
	if persona != "" {
		// Security: a model-supplied persona may only NAME a trusted persona,
		// never a path — it must not point a sub-agent at an arbitrary file.
		if strings.ContainsAny(persona, `/\`) || strings.HasSuffix(persona, ".md") {
			return toolErr("swarm_spawn: persona must be a built-in/installed persona NAME, not a path"), nil
		}
		if t.PersonaResolver != nil {
			resolved, err := t.PersonaResolver(persona)
			if err != nil {
				return toolErr("swarm_spawn: " + err.Error()), nil
			}
			persona = resolved
		}
	}

	route, errMsg := resolveSpawnRoute(a, t.HostProvider, t.HostModel, t.Tiers)
	if errMsg != "" {
		return toolErr("swarm_spawn: " + errMsg), nil
	}

	agent, err := t.Swarm.SpawnReq(ctx, swarm.SpawnRequest{
		Task:     task,
		Model:    route.Model,
		Provider: route.Provider,
		Persona:  persona,
	})
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("swarm_spawn: %w", err)
	}
	if t.OnSpawned != nil {
		t.OnSpawned(agent, task)
	}

	tier := strings.ToLower(strings.TrimSpace(a.Tier))
	var sb strings.Builder
	fmt.Fprintf(&sb, "spawned sub-agent %s\n", agent.ID)
	fmt.Fprintf(&sb, "task: %s\n", truncateTask(task, 200))
	if persona != "" {
		fmt.Fprintf(&sb, "persona: %s\n", persona)
	}
	switch {
	case route.TierModel != "":
		fmt.Fprintf(&sb, "model: %s (tier %s)\n", route.Model, tier)
	case tier != "" && route.Inherited:
		fmt.Fprintf(&sb, "model: %s (tier %s unavailable for %s; host model)\n", route.Model, tier, route.Provider)
	case route.Inherited && route.Model != "":
		fmt.Fprintf(&sb, "model: %s (inherited from host)\n", route.Model)
	case route.Model != "":
		fmt.Fprintf(&sb, "model: %s\n", route.Model)
	}
	if route.Provider != "" {
		if route.Inherited {
			fmt.Fprintf(&sb, "provider: %s (inherited from host)\n", route.Provider)
		} else {
			fmt.Fprintf(&sb, "provider: %s\n", route.Provider)
		}
	}
	sb.WriteString("\nThe sub-agent is running in the background. Use /swarm in the TUI to monitor it. ")
	sb.WriteString("This conversation continues immediately; do not wait for the sub-agent to finish before working on the next thing.")
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: sb.String()}},
		Details: map[string]any{
			"agent_id": agent.ID,
			"task":     task,
			"persona":  persona,
			"model":    route.Model,
			"tier":     tier,
			"provider": route.Provider,
		},
	}, nil
}

// spawnRoute is the resolved (provider, model) the sub-agent runs on. The
// two fields are a pair: a model id is only meaningful with its provider.
type spawnRoute struct {
	Model     string
	Provider  string
	Inherited bool   // both model+provider were omitted → inherits the host route
	TierModel string // the tier-resolved model, when a tier produced one
}

// resolveSpawnRoute pins the sub-agent's (model, provider) pair. An explicit
// model+provider passes through; omitting both inherits the host's resolved
// route so the sub-agent shares the parent session's provider/model/auth,
// with an optional tier picking a cheaper model FOR the host provider and
// the provider pinned to match (an unresolved tier falls back to the host
// model, still on the host provider). A lone model or provider is rejected:
// the sub-agent must not guess the missing partner or infer a provider from
// a model id. Pure (no I/O) so it is unit-testable.
func resolveSpawnRoute(a swarmSpawnArgs, hostProvider, hostModel string, tiers SwarmTierMap) (route spawnRoute, errMsg string) {
	model := strings.TrimSpace(a.Model)
	providerID := strings.TrimSpace(a.Provider)
	tier := strings.ToLower(strings.TrimSpace(a.Tier))

	if (model == "") != (providerID == "") {
		return spawnRoute{}, "set model and provider together, or omit both (optionally with a tier) to inherit the host session"
	}
	if model == "" && providerID == "" {
		route.Inherited = true
		providerID = strings.TrimSpace(hostProvider)
		if tier != "" {
			route.TierModel = ResolveSwarmTier(hostProvider, hostModel, tier, tiers)
			model = route.TierModel
		}
		if model == "" {
			model = strings.TrimSpace(hostModel)
		}
	}
	route.Model = model
	route.Provider = providerID
	return route, ""
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
