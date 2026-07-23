package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
	// hostMu guards live mutation of HostProvider/HostModel by SetHost when
	// the host session swaps models mid-turn; Execute reads them through
	// host() under the read lock. Construction sets the fields directly,
	// before the tool is registered, so those writes need no lock.
	hostMu sync.RWMutex

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

	// Personas is the dispatchable persona names, injected as the `persona`
	// argument's schema enum so the model can only pick a real specialist (and
	// gets validation for free). Empty leaves `persona` a free string. The
	// human-readable roster (what each persona is good for) rides the system
	// prompt when the proactive nudge is on; this is just the value set.
	Personas []string

	// Trusted reads the live Workspace Trust verdict for the cwd the
	// sub-agents would run in. An untrusted workspace degrades children
	// silently — they boot without project extensions, skills, or context
	// files, and only a stderr line in their own event log says so — so
	// Execute refuses the spawn with guidance to have the user run
	// `terva trust` (or pass allow_untrusted to proceed degraded). Nil
	// means the host doesn't track trust; the gate is skipped.
	Trusted func() bool

	// AllowBackend gates and validates the `backend` argument — a request to run
	// the task on a NON-terva agent (see the worker package) instead of a native
	// sub-agent. The host supplies it: this package cannot import the worker
	// registry (that would cycle through build), so the host injects a closure
	// that reads the external-workers config gate LIVE and validates the name
	// against the registered backends. Returning an error rejects the spawn with
	// that message; returning nil allows it. Nil means this host offers no
	// external backends at all, and any `backend` request is refused.
	AllowBackend func(name string) error
}

// SetHost updates the host provider/model this tool inherits for tier
// resolution and omitted-route spawns. Safe to call while a turn runs: a
// mid-session model swap mutates this live, and Execute reads through
// host() under the read lock. Implements HostRouted.
func (t *SwarmSpawnTool) SetHost(provider, model string) {
	t.hostMu.Lock()
	defer t.hostMu.Unlock()
	t.HostProvider = provider
	t.HostModel = model
}

func (t *SwarmSpawnTool) host() (provider, model string) {
	t.hostMu.RLock()
	defer t.hostMu.RUnlock()
	return t.HostProvider, t.HostModel
}

type swarmSpawnArgs struct {
	Task           string `json:"task"`
	Persona        string `json:"persona,omitempty"`
	Model          string `json:"model,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Tier           string `json:"tier,omitempty"`
	AllowUntrusted bool   `json:"allow_untrusted,omitempty"`
	Backend        string `json:"backend,omitempty"`
	// DeliverableSchema is the structured-deliverable contract: a JSON
	// schema the sub-agent's report must match (see SpawnRequest.Schema).
	DeliverableSchema json.RawMessage `json:"deliverable_schema,omitempty"`
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
      "description": "Optional persona to boot the sub-agent as a specialist. Pick the one whose focus matches the sub-task (your instructions list what each is good for when there are any); omit for a general-purpose sub-agent."
    },
    "deliverable_schema": {
      "type": "object",
      "description": "Optional JSON schema the sub-agent's report must match (top-level \"type\" must be \"object\"). The sub-agent gets a deliver_result tool bound to it, and the validated JSON is surfaced in the recap and on the swarm dashboard. Use when you will machine-read the result (findings lists, counts, verdicts); omit for prose answers."
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
    },
    "allow_untrusted": {
      "type": "boolean",
      "description": "Set true ONLY after the user has explicitly declined to trust this workspace but still wants sub-agents: spawns them degraded (no project extensions, skills, or context files). Normally leave unset — if the workspace is untrusted, ask the user to run 'terva trust' first."
    },
    "backend": {
      "type": "string",
      "description": "Optional. Hand this task to an EXTERNAL coding agent instead of a native terva sub-agent — e.g. \"claude\" for Claude Code. The external agent works in its own leased checkout, brings its own tools and credentials, and reports back its result the same way a native sub-agent does. Only available when the user has enabled external workers; omit for a normal terva sub-agent (the right default almost always)."
    }
  },
  "required": ["task"]
}`

func (t *SwarmSpawnTool) Name() string { return "swarm_spawn" }
func (t *SwarmSpawnTool) Description() string {
	return "Spawn a background sub-agent to work on an independent sub-task in parallel. Returns the sub-agent's id immediately and keeps running while this conversation continues — do NOT wait for it before moving on to your next piece of work. The sub-agent shares this working directory and has the same tools, but starts with NO context from this conversation, so give it a self-contained task description. Good for splitting genuinely independent work (write the tests while implementing the feature; investigate three files at once); not for trivial single-step work, for steps that depend on each other in sequence, or when the user asked you to do the work yourself. When every sub-agent you spawned finishes, you'll receive a single [auto-swarm update] recapping each one's outcome to summarize."
}

// Schema injects the dispatchable persona names as the `persona` enum when the
// host supplies them, so the model can only pick a real specialist and gets
// validation for free; with none it stays a free string.
func (t *SwarmSpawnTool) Schema() json.RawMessage {
	if len(t.Personas) == 0 {
		return json.RawMessage(swarmSpawnSchema)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(swarmSpawnSchema), &m); err != nil {
		return json.RawMessage(swarmSpawnSchema)
	}
	if props, ok := m["properties"].(map[string]any); ok {
		if p, ok := props["persona"].(map[string]any); ok {
			p["enum"] = t.Personas
		}
	}
	if out, err := json.Marshal(m); err == nil {
		return out
	}
	return json.RawMessage(swarmSpawnSchema)
}

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

	// Untrusted workspaces degrade sub-agents silently (no project
	// extensions/skills/context files), so surface it HERE, where the
	// calling agent can relay it and the user can fix it — not as a
	// stderr line buried in the child's own event log.
	untrusted := t.Trusted != nil && !t.Trusted()
	if untrusted && !a.AllowUntrusted {
		return toolErr("swarm_spawn: this workspace is untrusted, so sub-agents would run WITHOUT its project extensions, skills, and context files. Ask the user to trust it (run `terva trust`, or restart and accept the trust prompt), then retry. If the user explicitly wants degraded sub-agents instead, retry with allow_untrusted: true."), nil
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

	hostProvider, hostModel := t.host()
	route, errMsg := resolveSpawnRoute(a, hostProvider, hostModel, t.Tiers)
	if errMsg != "" {
		return toolErr("swarm_spawn: " + errMsg), nil
	}

	// A request to run this task on an external agent is gated and validated by
	// the host (the config gate is read live there, and the name is checked
	// against the registered backends). A native sub-agent — the overwhelming
	// default — leaves this empty and the whole check is skipped.
	backend := strings.TrimSpace(a.Backend)
	if backend != "" {
		if t.AllowBackend == nil {
			return toolErr("swarm_spawn: external worker backends are not available in this host; omit `backend` to spawn a native sub-agent"), nil
		}
		if err := t.AllowBackend(backend); err != nil {
			return toolErr("swarm_spawn: " + err.Error()), nil
		}
	}

	// The deliverable schema becomes the child's deliver_result argument
	// schema verbatim, so it must parse and be a top-level object (a
	// provider requirement for tool schemas) BEFORE anything spawns.
	if len(a.DeliverableSchema) > 0 {
		var m map[string]any
		if err := json.Unmarshal(a.DeliverableSchema, &m); err != nil {
			return toolErr(fmt.Sprintf("swarm_spawn: deliverable_schema does not parse: %v", err)), nil
		}
		if typ, _ := m["type"].(string); typ != "object" {
			return toolErr(`swarm_spawn: deliverable_schema must declare "type": "object" at the top level (it becomes the sub-agent's deliver_result argument schema)`), nil
		}
	}

	req := swarm.SpawnRequest{
		Task:     task,
		Model:    route.Model,
		Provider: route.Provider,
		Persona:  persona,
		Backend:  backend,
		Schema:   a.DeliverableSchema,
	}
	// Stamp the spawn with the host conversation's session id so the child's
	// meta.json records which conversation it belongs to and the /swarm
	// dashboard can scope to it. The dispatch context carries the calling
	// agent; a live-only conversation (no transcript) leaves the stamp empty.
	if ag := core.AgentFromContext(ctx); ag != nil {
		if sid, _ := ag.SessionIdentity(); sid != "" {
			req.SessionID = sid
		}
	}
	agent, err := t.Swarm.SpawnReq(ctx, req)
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
	if backend != "" {
		// Name it so the coordinator knows this one runs elsewhere — its result
		// comes back the same way, but it is not a native terva sub-agent.
		fmt.Fprintf(&sb, "backend: %s (external worker)\n", backend)
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
	if untrusted {
		sb.WriteString("note: UNTRUSTED workspace — the sub-agent runs without project extensions, skills, or context files.\n")
	}
	// Lead with the fact that completion is PUSHED. "Once it has run,
	// session_inspect accepts <id>" read as an instruction to go and look,
	// and a caller with no way to wait invents one — polling the id, or
	// watching the child's files change from a shell. Neither is needed.
	sb.WriteString("\nThe sub-agent is running in the background. Use /swarm in the TUI to monitor it. ")
	sb.WriteString("You do NOT need to wait or poll for it: when its task finishes you are re-invoked automatically with an [auto-swarm update] carrying its findings. ")
	fmt.Fprintf(&sb, "If you do want to look in mid-run, session_inspect takes %q as a session_id and streams what the sub-agent has written so far (the id is NOT a project session id). ", agent.ID)
	sb.WriteString("This conversation continues immediately; work on the next thing, or end your turn if nothing else is pending.")
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
