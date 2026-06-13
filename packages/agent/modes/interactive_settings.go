package modes

// The /settings and /permissions surfaces: item builders, dialog
// plumbing, and the appliers each toggle dispatches to.

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func effectiveImageProtocol(override *bool) tui.ImageProtocol {
	detected := tui.DetectImageProtocol()
	if override == nil {
		return detected
	}
	if !*override {
		return tui.ImageProtocolNone
	}
	return detected
}

func imageProtocolName(p tui.ImageProtocol) string {
	switch p {
	case tui.ImageProtocolKitty:
		return "kitty/ghostty"
	case tui.ImageProtocolITerm2:
		return "iTerm2"
	default:
		return "none"
	}
}

func onOff(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func (i *Interactive) openSettingsDialog() {
	detected := tui.DetectImageProtocol()
	imgEnabled := detected != tui.ImageProtocolNone
	if i.cfg.InlineImagesEnabled != nil {
		imgEnabled = *i.cfg.InlineImagesEnabled
	}
	imgDisabled := detected == tui.ImageProtocolNone
	imgHint := ""
	if imgDisabled {
		imgEnabled = false
		imgHint = "this terminal does not support inline images"
	} else {
		imgHint = "terminal supports " + imageProtocolName(detected)
	}

	autoSwarm := false
	if i.cfg.AutoSwarmEnabled != nil {
		autoSwarm = *i.cfg.AutoSwarmEnabled
	}
	autoSwarmDisabled := i.cfg.Swarm == nil
	autoSwarmHint := ""
	if autoSwarmDisabled {
		autoSwarm = false
		autoSwarmHint = "swarm supervisor not available in this mode"
	}

	recursiveFiles := i.cfg.RecursiveFileSuggest != nil && *i.cfg.RecursiveFileSuggest
	respectGitignore := i.cfg.RespectGitignore == nil || *i.cfg.RespectGitignore

	reasoningOptions := []settingsOption{
		{value: "", label: "off", desc: "no reasoning"},
		{value: "minimum", label: "minimum", desc: "very brief (~1k tokens)"},
		{value: "low", label: "low", desc: "light (~2k tokens)"},
		{value: "medium", label: "medium", desc: "moderate (~8k tokens)"},
		{value: "high", label: "high", desc: "deep (~16k tokens)"},
		{value: "maximum", label: "maximum", desc: "highest (~32k tokens)"},
	}
	reasoning := provider.NormalizeReasoning(i.cfg.Reasoning)
	reasoningChoice := 0
	for idx, opt := range reasoningOptions {
		if opt.value == reasoning {
			reasoningChoice = idx
			break
		}
	}
	reasoningHint := ""
	if m, err := provider.FindModel(i.cfg.Provider, i.cfg.Model); err == nil && !m.Reasoning {
		reasoningHint = "current model does not support thinking"
	}

	themeName := i.cfg.ThemeName
	if themeName == "" {
		themeName = "auto"
	}
	if themeName != "auto" && !tui.ThemeExists(i.cfg.TervaHome, themeName) {
		themeName = "auto"
		i.cfg.ThemeName = ""
		if i.cfg.SettingsStore != nil {
			_ = i.cfg.SettingsStore.SetTheme("auto")
		}
		i.applyThemeNow("auto")
	}
	themeOptions := []settingsOption{}
	themeChoice := 0
	availableThemes := tui.AvailableThemes(i.cfg.TervaHome)
	if i.cfg.ExtensionThemes != nil {
		availableThemes = append(availableThemes, i.cfg.ExtensionThemes()...)
	}
	for idx, opt := range availableThemes {
		themeOptions = append(themeOptions, settingsOption{value: opt.Value, label: opt.Label, desc: opt.Description})
		if opt.Value == themeName {
			themeChoice = idx
		}
	}

	approvalItem, hasApproval := i.approvalSettingItem()

	items := []settingsItem{
		{
			key:      "inline_images_enabled",
			label:    "render images when supported",
			desc:     "draw screenshots inline instead of showing a text placeholder",
			value:    imgEnabled,
			disabled: imgDisabled,
			hint:     imgHint,
		},
		{
			key:      "auto_swarm_enabled",
			label:    "auto-swarm",
			desc:     "let the agent spawn background sub-agents in parallel via the swarm_spawn tool",
			value:    autoSwarm,
			disabled: autoSwarmDisabled,
			hint:     autoSwarmHint,
		},
		{
			key:   "recursive_file_suggest",
			label: "recursive @-file search",
			desc:  "fuzzy-search the whole project tree when picking files with @ instead of browsing one directory at a time",
			value: recursiveFiles,
		},
		{
			key:   "respect_gitignore",
			label: "hide gitignored files in @-picker",
			desc:  "skip files and directories matched by the project's root .gitignore (and .git) when picking files with @",
			value: respectGitignore,
		},
		{
			key:     "reasoning",
			label:   "thinking level",
			desc:    "reasoning depth for thinking-capable models",
			options: reasoningOptions,
			choice:  reasoningChoice,
			hint:    reasoningHint,
		},
		{
			key:     "theme",
			label:   "color theme",
			desc:    "choose a theme from $TERVA_HOME/themes or a loaded extension",
			options: themeOptions,
			choice:  themeChoice,
		},
	}
	if hasApproval {
		items = append(items, approvalItem)
	}
	i.settingsDialog.Open(items)
}

// openPermissionsDialog builds and shows the read-only /permissions
// inspector: current approval mode, the active rules grouped by source
// (user/project/extension), and this session's "always allow" grants.
func (i *Interactive) openPermissionsDialog() {
	info, grants := i.buildPermissionsView()
	i.permissionsDialog.Open(info, grants)
	i.invalidate()
}

// refreshPermissionsDialog rebuilds the inspector's content from the
// live gate after a revoke, keeping the dialog open with a clamped
// cursor so the list stays usable as it shrinks.
func (i *Interactive) refreshPermissionsDialog() {
	info, grants := i.buildPermissionsView()
	i.permissionsDialog.Refresh(info, grants)
}

// buildPermissionsView formats the gate's mode and rules into
// theme-colored static lines (info) and returns this session's "always
// allow" grants as a selectable list. Reads everything from the live
// gate so it always reflects the current state.
func (i *Interactive) buildPermissionsView() (info []string, grants []permGrant) {
	th := i.cfg.Theme
	gate := i.cfg.ConfirmGate
	if gate == nil {
		return []string{th.FG256(th.Muted, "no permission gate (yolo): every tool runs without asking.")}, nil
	}

	var out []string
	out = append(out, th.FG256(th.Accent, tui.Bold("approval mode"))+"  "+string(gate.Mode()))
	out = append(out, th.FG256(th.Muted, "  change it in /settings; flags --approval / --no-yolo set the startup default."))
	out = append(out, "")

	decColor := func(d core.RuleDecision) string {
		switch d {
		case core.RuleAllow:
			return th.FG256(th.Accent, string(d))
		case core.RuleDeny:
			return th.FG256(th.Error, string(d))
		default:
			return th.FG256(th.Warning, string(d))
		}
	}

	rules := gate.Rules()
	if len(rules) == 0 {
		out = append(out, th.FG256(th.Muted, "no permission rules in effect."))
	} else {
		out = append(out, th.FG256(th.Accent, tui.Bold("rules"))+th.FG256(th.Muted, "  (first match wins; user → project → extension)"))
		lastSrc := ""
		for _, r := range rules {
			if r.Source != lastSrc {
				out = append(out, th.FG256(th.Muted, "  ["+r.Source+"]"))
				lastSrc = r.Source
			}
			line := "    " + decColor(r.Decision) + "  " + r.Tool
			if r.Args != nil {
				line += th.FG256(th.Muted, "  args~/"+r.Args.String()+"/")
			}
			if r.Reason != "" {
				line += th.FG256(th.Muted, "  — "+r.Reason)
			}
			out = append(out, line)
		}
	}
	out = append(out, "")

	out = append(out, th.FG256(th.Accent, tui.Bold("this session")))
	allowAll, tools := gate.Grants()
	if allowAll {
		grants = append(grants, permGrant{allowAll: true})
	}
	for _, t := range tools {
		grants = append(grants, permGrant{tool: t})
	}
	if len(grants) == 0 {
		out = append(out, "  "+th.FG256(th.Muted, "no session grants yet."))
	}
	return out, grants
}

// approvalModeLabel returns the live approval mode for the status bar,
// or "" when there's no gate (so the bar falls back to its legacy
// NoYolo tag). "yolo" renders no badge.
func (i *Interactive) approvalModeLabel() string {
	if i.cfg.ConfirmGate == nil {
		return ""
	}
	return string(i.cfg.ConfirmGate.Mode())
}

// approvalSettingItem builds the /settings approval-mode picker from
// the live gate, or reports false when live switching isn't wired
// (no gate or no rebuild callback — embedders/tests). The choice is
// read from the gate so reopening /settings always reflects the
// current mode.
func (i *Interactive) approvalSettingItem() (settingsItem, bool) {
	if i.cfg.ConfirmGate == nil || i.cfg.SetApprovalMode == nil {
		return settingsItem{}, false
	}
	opts := []settingsOption{
		{value: string(core.ApprovalPlan), label: "plan", desc: "read-only tools only; mutating tools are withheld"},
		{value: string(core.ApprovalAsk), label: "ask", desc: "confirm every tool call"},
		{value: string(core.ApprovalAutoEdit), label: "auto-edit", desc: "run reads + file edits; confirm everything else"},
		{value: string(core.ApprovalWorkspace), label: "workspace", desc: "run built-in tools + read-only tools; confirm side-effecting extension/MCP tools"},
		{value: string(core.ApprovalYolo), label: "yolo", desc: "run every tool without asking"},
	}
	cur := string(i.cfg.ConfirmGate.Mode())
	choice := 0
	for idx, o := range opts {
		if o.value == cur {
			choice = idx
			break
		}
	}
	return settingsItem{
		key:     "approval_mode",
		label:   "approval mode",
		desc:    "how much the agent may do without asking (see /permissions)",
		options: opts,
		choice:  choice,
	}, true
}

func (i *Interactive) applySettingChange(act settingsAction) {
	switch act.Key {
	case "reasoning":
		i.applyReasoningSetting(act.StringValue)
	case "theme":
		i.applyThemeSetting(act.StringValue)
	case "approval_mode":
		i.applyApprovalModeSetting(act.StringValue)
	default:
		i.applySettingToggle(act.Key, act.Value)
	}
}

// applyApprovalModeSetting switches the approval mode for THIS SESSION:
// the cli callback swaps enforcement on the gate and returns the tool
// registry rebuilt for the new mode (plan withholds mutating tools),
// which we install on the running agent. Deliberately NOT persisted —
// the approval mode is a security posture like /jail, not a preference
// like theme. The persistent default comes only from an explicit
// `approval` key in config or the --approval flag, so the picker can
// never silently pin a mode (least of all the current default) into
// config and mask a future default change.
func (i *Interactive) applyApprovalModeSetting(value string) {
	defer i.invalidate()
	mode, err := core.ParseApprovalMode(value)
	if err != nil || i.cfg.SetApprovalMode == nil {
		return
	}
	reg := i.cfg.SetApprovalMode(mode)
	i.mu.Lock()
	if ag := i.turns.Agent(); ag != nil && reg != nil {
		ag.SetTools(reg)
	}
	i.statusOK = "approval mode " + value + " (this session)"
	i.statusErr = ""
	i.mu.Unlock()
}

func (i *Interactive) applySettingToggle(key string, value bool) {
	// Every setting toggle forces a full repaint at the end — same
	// effect as the user pressing Ctrl+L — so any per-setting visual
	// change (image rendering, status copy, future toggles) lands
	// immediately instead of waiting for the next diff frame.
	defer func() {
		if i.rend != nil {
			i.rend.Clear()
		}
		i.invalidate()
	}()
	switch key {
	case "inline_images_enabled":
		val := value
		i.cfg.InlineImagesEnabled = &val
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetInlineImages(value); err != nil {
				i.mu.Lock()
				i.statusErr = "settings: " + err.Error()
				i.mu.Unlock()
				return
			}
		}
		i.mu.Lock()
		i.view.ImageProto = effectiveImageProtocol(i.cfg.InlineImagesEnabled)
		i.view.InvalidateRenderCache()
		i.statusOK = "inline image rendering " + onOff(value)
		i.statusErr = ""
		i.mu.Unlock()
	case "auto_swarm_enabled":
		val := value
		i.cfg.AutoSwarmEnabled = &val
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetAutoSwarm(value); err != nil {
				i.mu.Lock()
				i.statusErr = "settings: " + err.Error()
				i.mu.Unlock()
				return
			}
		}
		// Add/remove the swarm_spawn tool on the live agent so the
		// model's tools[] list reflects the toggle on the next turn.
		// Without this the tool stays advertised after a disable and
		// the model keeps trying to call it.
		i.applyAutoSwarmTool(value)
		// Also swap the system-prompt addendum in/out so the model
		// knows to use the tool proactively (or stops referencing it
		// after a disable).
		i.applyAutoSwarmSystemPrompt(value)
		i.mu.Lock()
		i.statusOK = "auto-swarm " + onOff(value)
		i.statusErr = ""
		i.mu.Unlock()
	case "recursive_file_suggest":
		val := value
		i.cfg.RecursiveFileSuggest = &val
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetRecursiveFileSuggest(value); err != nil {
				i.mu.Lock()
				i.statusErr = "settings: " + err.Error()
				i.mu.Unlock()
				return
			}
		}
		// Flip the live picker so the next @ reflects the new mode
		// without restarting terva. SetRecursive drops its cache.
		i.fileSuggest.SetRecursive(value)
		i.mu.Lock()
		i.statusOK = "recursive @-file search " + onOff(value)
		i.statusErr = ""
		i.mu.Unlock()
	case "respect_gitignore":
		val := value
		i.cfg.RespectGitignore = &val
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetRespectGitignore(value); err != nil {
				i.mu.Lock()
				i.statusErr = "settings: " + err.Error()
				i.mu.Unlock()
				return
			}
		}
		i.fileSuggest.SetRespectGitignore(value)
		i.mu.Lock()
		i.statusOK = "hide gitignored files in @-picker " + onOff(value)
		i.statusErr = ""
		i.mu.Unlock()
	}
}

func (i *Interactive) applyThemeSetting(name string) {
	if i.cfg.SettingsStore != nil {
		if err := i.cfg.SettingsStore.SetTheme(name); err != nil {
			i.mu.Lock()
			i.statusErr = "settings: " + err.Error()
			i.mu.Unlock()
			return
		}
	}
	i.cfg.ThemeName = name
	if name == "auto" {
		i.cfg.ThemeName = ""
	}
	i.applyThemeNow(name)
}

func (i *Interactive) applyThemeNow(name string) {
	if name == "" {
		name = "auto"
	}
	detected := tui.Dark
	if tui.IsLightTheme(i.cfg.Theme) {
		detected = tui.Light
	}
	th, applied, err := tui.LoadThemeFromHome(i.cfg.TervaHome, name, detected)
	if err != nil {
		if i.cfg.SettingsStore != nil {
			_ = i.cfg.SettingsStore.SetTheme("auto")
		}
		i.cfg.ThemeName = ""
		th, applied, _ = tui.LoadThemeFromHome(i.cfg.TervaHome, "auto", detected)
		i.mu.Lock()
		i.statusErr = "theme missing; reset to default"
		i.mu.Unlock()
	} else {
		i.mu.Lock()
		label := applied
		if label == "" {
			label = "auto"
		}
		i.statusOK = "theme " + label
		i.statusErr = ""
		i.mu.Unlock()
	}
	i.cfg.Theme = th
	// The agent-goroutine event sink also mutates i.view under i.mu,
	// so view writes here must take the same lock; ed/spin/rend are
	// main-loop-only and just ride along inside it.
	i.mu.Lock()
	i.view.Theme = th
	i.view.InvalidateRenderCache()
	i.ed.Prompt = th.AccentBar(th.Accent)
	i.spin.Configure(th)
	if i.rend != nil {
		i.rend.SetTheme(th)
		i.rend.Clear()
	}
	i.mu.Unlock()
	i.invalidate()
}

func (i *Interactive) applyReasoningSetting(level string) {
	defer func() {
		if i.rend != nil {
			i.rend.Clear()
		}
		i.invalidate()
	}()
	level = provider.NormalizeReasoning(level)
	i.cfg.Reasoning = level
	if i.cfg.SettingsStore != nil {
		if err := i.cfg.SettingsStore.SetReasoning(level); err != nil {
			i.mu.Lock()
			i.statusErr = "settings: " + err.Error()
			i.mu.Unlock()
			return
		}
	}
	i.mu.Lock()
	if ag := i.turns.Agent(); ag != nil {
		ag.Reasoning = level
	}
	label := level
	if label == "" {
		label = "off"
	}
	i.statusOK = "thinking level " + label
	i.statusErr = ""
	i.mu.Unlock()
}
