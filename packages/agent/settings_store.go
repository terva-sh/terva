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

func (configSettingsStore) SetAutoSwarmNudge(enabled bool) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.AutoSwarmNudge = &enabled
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

// AutoSwarmNudgeEnabled reports whether the proactive-delegation nudge (the
// swarm system addendum) should be injected. Independent of AutoSwarmEnabled
// and defaults ON (nil = true), so enabling auto-swarm keeps today's behavior;
// set auto_swarm_nudge=false to keep the tool but drop the nudge.
func AutoSwarmNudgeEnabled() bool {
	cfg, err := LoadConfig()
	if err != nil {
		return true
	}
	return cfg.AutoSwarmNudge == nil || *cfg.AutoSwarmNudge
}

// AutoSwarmSystemAddendum is the proactive-delegation NUDGE (Toggle 2): the one
// disposition that isn't self-evident from the swarm_spawn tool description. The
// tool's mechanics (self-contained tasks, no inherited context, don't block,
// when-NOT-to-use, the [auto-swarm update] recap) live in the tool description +
// the recap message; valid persona names live in the tool's schema enum. So this
// stays short — just the "reach for it proactively" push a bare tool wouldn't
// give. See docs/proposals/web-i18n-authoring.md (sibling auto-swarm discussion).
const AutoSwarmSystemAddendum = `When a request naturally splits into independent sub-tasks that can run concurrently, reach for swarm_spawn proactively rather than doing everything sequentially yourself — spawn one sub-agent per independent task and keep the coordinating work moving in parallel.`
