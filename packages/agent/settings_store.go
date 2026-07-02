package agent

import (
	"strings"
	"time"

	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/provider"
)

type configSettingsStore struct{}

// statusScriptsForTUI converts the user config's status_line.scripts
// into the modes-side shape: lowercased names, empty commands dropped.
// Reading only from the user config layer (LoadConfig) is what keeps
// script segments on the Hooks trust rule — never ProjectConfig, and a
// project-scoped home only exists once the workspace was trusted.
func statusScriptsForTUI(cfg Config) map[string]modes.StatusScript {
	src := cfg.StatusLineScripts()
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]modes.StatusScript, len(src))
	for name, s := range src {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || strings.TrimSpace(s.Command) == "" {
			continue
		}
		out[name] = modes.StatusScript{
			Command: s.Command,
			Timeout: time.Duration(s.TimeoutMS) * time.Millisecond,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

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

// SetStatusLineRows persists the status-bar segment layout. nil clears
// back to the built-in defaults while preserving any configured
// status_line.scripts (the block is only dropped when nothing else
// lives in it).
func (configSettingsStore) SetStatusLineRows(rows [][]string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	switch {
	case rows == nil:
		if cfg.StatusLine != nil {
			cfg.StatusLine.Rows = nil
			if len(cfg.StatusLine.Scripts) == 0 {
				cfg.StatusLine = nil
			}
		}
	default:
		if cfg.StatusLine == nil {
			cfg.StatusLine = &StatusLineConfig{}
		}
		cfg.StatusLine.Rows = rows
	}
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

// SetUserName persists what a character card's {{user}} macro resolves to (the
// name the user wants to be addressed by). It writes to the GLOBAL config so the
// preference follows the user across projects (surviving project-scoping) rather
// than being saved into a repo's redirected home. Empty clears it.
func (configSettingsStore) SetUserName(name string) error {
	return SetGlobalUserName(name)
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
