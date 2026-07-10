package agent

// The `terva replay <file>` entry point: play a recorded session transcript
// back through the interactive TUI as a deterministic, transport-controlled
// scene (docs/proposals/session-player.md). It backs the TUI with a read-only
// replay.Carrier instead of a live Workspace — no credential, no live turns.
// The transcript animates from the file as the same ctrlproto event stream a
// live session emits, so the TUI renders it unchanged; the render path keys off
// cfg.Carrier and every crutch reader guards a nil agent, so Agent is nil here.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/replay"
	"terva.sh/terva/packages/tui"
)

// runReplayMode opens the recorded session at args.ReplayPath and runs the TUI
// against a replay carrier. Playback autostarts when the TUI subscribes; typed
// input is inert (the carrier has no agent, so the composer's submit gate
// blocks it, and Prompt/Queue reject with CodeUnsupported anyway).
func runReplayMode(ctx context.Context, args build.Args, version string) error {
	path := args.ReplayPath
	if path == "" {
		return fmt.Errorf("usage: terva replay <session.jsonl>")
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}

	carrier, err := replay.Open(path, replay.Options{Autoplay: true})
	if err != nil {
		return fmt.Errorf("open replay %s: %w", path, err)
	}
	defer carrier.Close()

	info, err := carrier.ResumeSession(ctx, "")
	if err != nil {
		return err
	}

	initialCfg, _ := config.LoadConfig()
	theme, _, themeErr := tui.DetectThemeWithCustom(config.TervaHome(), initialCfg.Theme, 80*time.Millisecond)
	if themeErr != nil {
		fmt.Fprintln(os.Stderr, "theme load:", themeErr)
	}
	cwd, _ := os.Getwd()

	iv := modes.NewInteractive(modes.InteractiveConfig{
		Terminal:            tui.NewProcTerm(),
		Theme:               theme,
		ThemeName:           initialCfg.Theme,
		InlineImagesEnabled: initialCfg.InlineImagesEnabled,
		StatusLineRows:      initialCfg.StatusLineRows(),
		StatusScripts:       statusScriptsForTUI(initialCfg),
		SettingsStore:       configSettingsStore{},
		Model:               info.Model,
		Provider:            info.Provider,
		CWD:                 cwd,
		TervaHome:           config.TervaHome(),
		Version:             version,
		Ready:               false, // read-only replay: no prompting, the transport replaces the turn loop
		Carrier:             carrier,
		CarrierSession:      info.ID,
		// Session/control closures are intentionally omitted: they are all
		// nil-guarded, so their slash commands degrade to a no-op in a replay.
	})
	return iv.Run(ctx)
}
