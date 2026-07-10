package agent

import (
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modes"
)

type configSettingsStore struct{}

// statusScriptsForTUI converts the user config's status_line.scripts
// into the modes-side shape: lowercased names, empty commands dropped.
// Reading only from the user config layer (LoadConfig) is what keeps
// script segments on the Hooks trust rule — never ProjectConfig, and a
// project-scoped home only exists once the workspace was trusted.
func statusScriptsForTUI(cfg config.Config) map[string]modes.StatusScript {
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
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	cfg.InlineImagesEnabled = &enabled
	return config.SaveConfig(cfg)
}

func (configSettingsStore) SetRecursiveFileSuggest(enabled bool) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	cfg.RecursiveFileSuggest = &enabled
	return config.SaveConfig(cfg)
}

func (configSettingsStore) SetRespectGitignore(enabled bool) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	cfg.RespectGitignore = &enabled
	return config.SaveConfig(cfg)
}

// SetStatusLineRows persists the status-bar segment layout. nil clears
// back to the built-in defaults while preserving any configured
// status_line.scripts (the block is only dropped when nothing else
// lives in it).
func (configSettingsStore) SetStatusLineRows(rows [][]string) error {
	cfg, err := config.LoadConfig()
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
			cfg.StatusLine = &config.StatusLineConfig{}
		}
		cfg.StatusLine.Rows = rows
	}
	return config.SaveConfig(cfg)
}

func (configSettingsStore) SetTheme(name string) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	if name == "auto" {
		name = ""
	}
	cfg.Theme = name
	return config.SaveConfig(cfg)
}

// SetUserName persists what a character card's {{user}} macro resolves to (the
// name the user wants to be addressed by). It writes to the GLOBAL config so the
// preference follows the user across projects (surviving project-scoping) rather
// than being saved into a repo's redirected home. Empty clears it.
func (configSettingsStore) SetUserName(name string) error {
	return config.SetGlobalUserName(name)
}
