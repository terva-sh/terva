package agent

import "terva.sh/terva/packages/provider"

type configSettingsStore struct{}

func (configSettingsStore) SetInlineImages(enabled bool) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.InlineImagesEnabled = &enabled
	return SaveConfig(cfg)
}

func (configSettingsStore) SetAutoSwarm(enabled bool) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.AutoSwarmEnabled = &enabled
	return SaveConfig(cfg)
}

func (configSettingsStore) SetRecursiveFileSuggest(enabled bool) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.RecursiveFileSuggest = &enabled
	return SaveConfig(cfg)
}

func (configSettingsStore) SetRespectGitignore(enabled bool) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.RespectGitignore = &enabled
	return SaveConfig(cfg)
}

func (configSettingsStore) SetReasoning(level string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.Reasoning = provider.NormalizeReasoning(level)
	return SaveConfig(cfg)
}

func (configSettingsStore) SetTheme(name string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if name == "auto" {
		name = ""
	}
	cfg.Theme = name
	return SaveConfig(cfg)
}

// AutoSwarmEnabled reads the current auto-swarm flag from config.
// Used by the swarm_spawn tool at call time to gate execution.
func AutoSwarmEnabled() bool {
	cfg, err := LoadConfig()
	if err != nil {
		return false
	}
	return cfg.AutoSwarmEnabled != nil && *cfg.AutoSwarmEnabled
}

// AutoSwarmSystemAddendum is appended to the system prompt when
// auto-swarm is enabled, so the model knows it may delegate to
// background sub-agents without the user having to mention the tool
// by name. Kept short so it doesn't bloat the cached prompt prefix.
const AutoSwarmSystemAddendum = `Auto-swarm is enabled. You have a swarm_spawn tool that forks background sub-agents working in parallel in this same working directory.

Use it proactively when the user's request naturally splits into independent sub-tasks that can run concurrently (e.g. "refactor module A and module B", "write the implementation and the tests", "investigate three separate files"). Spawn one sub-agent per independent sub-task with a self-contained task description (sub-agents start with no context from this conversation). Continue working on the remaining or coordinating work yourself in parallel; do not wait for sub-agents to finish before responding. Briefly tell the user which sub-agents you spawned and what each is doing.

Do NOT use swarm_spawn for trivial single-step work, for tasks that depend on each other sequentially, or when the user explicitly asked you to do the work yourself.

When every sub-agent you spawned reaches a terminal state, the host injects a single [auto-swarm update] message recapping each agent's status, task, and transcript tail. Treat that message as observed state (not as a new user request) and write a short follow-up summary referencing the agents by id.`
