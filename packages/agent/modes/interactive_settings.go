package modes

// The /settings and /permissions surfaces: item builders, dialog
// plumbing, and the appliers each toggle dispatches to.

import (
	"context"
	"strconv"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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
		return i18n.T("enabled")
	}
	return i18n.T("disabled")
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
		imgHint = i18n.T("this terminal does not support inline images")
	} else {
		imgHint = i18n.T("terminal supports %s", imageProtocolName(detected))
	}

	autoSwarm := false
	if i.cfg.AutoSwarmEnabled != nil {
		autoSwarm = *i.cfg.AutoSwarmEnabled
	}
	// On the carrier the swarm supervisor lives daemon-side, so the local
	// Swarm handle is nil by design — the tasks-surface gate (CarrierTasks,
	// same withholding as legacy cfg.Swarm) decides availability instead.
	autoSwarmDisabled := i.cfg.Swarm == nil
	if i.cfg.Carrier != nil {
		autoSwarmDisabled = !i.cfg.CarrierTasks
	}
	autoSwarmHint := ""
	if autoSwarmDisabled {
		autoSwarm = false
		autoSwarmHint = i18n.T("swarm supervisor not available in this mode")
	}

	recursiveFiles := i.cfg.RecursiveFileSuggest != nil && *i.cfg.RecursiveFileSuggest
	respectGitignore := i.cfg.RespectGitignore == nil || *i.cfg.RespectGitignore

	reasoningOptions := []settingsOption{
		{value: "", label: i18n.T("off"), desc: i18n.T("no reasoning")},
		{value: "minimum", label: i18n.T("minimum"), desc: i18n.T("very brief (~1k tokens)")},
		{value: "low", label: i18n.T("low"), desc: i18n.T("light (~2k tokens)")},
		{value: "medium", label: i18n.T("medium"), desc: i18n.T("moderate (~8k tokens)")},
		{value: "high", label: i18n.T("high"), desc: i18n.T("deep (~16k tokens)")},
		{value: "maximum", label: i18n.T("maximum"), desc: i18n.T("highest (~32k tokens)")},
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
		reasoningHint = i18n.T("current model does not support thinking")
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
			label:    i18n.T("render images when supported"),
			desc:     i18n.T("draw screenshots inline instead of showing a text placeholder"),
			value:    imgEnabled,
			disabled: imgDisabled,
			hint:     imgHint,
		},
		{
			key:      "auto_swarm_enabled",
			label:    i18n.T("auto-swarm"),
			desc:     i18n.T("let the agent spawn background sub-agents in parallel via the swarm_spawn tool"),
			value:    autoSwarm,
			disabled: autoSwarmDisabled,
			hint:     autoSwarmHint,
		},
		{
			key:   "recursive_file_suggest",
			label: i18n.T("recursive @-file search"),
			desc:  i18n.T("fuzzy-search the whole project tree when picking files with @ instead of browsing one directory at a time"),
			value: recursiveFiles,
		},
		{
			key:   "respect_gitignore",
			label: i18n.T("hide gitignored files in @-picker"),
			desc:  i18n.T("skip files and directories matched by the project's root .gitignore (and .git) when picking files with @"),
			value: respectGitignore,
		},
		{
			key:     "reasoning",
			label:   i18n.T("thinking level"),
			desc:    i18n.T("reasoning depth for thinking-capable models"),
			options: reasoningOptions,
			choice:  reasoningChoice,
			hint:    reasoningHint,
		},
		{
			key:     "theme",
			label:   i18n.T("color theme"),
			desc:    i18n.T("choose a theme from $TERVA_HOME/themes or a loaded extension"),
			options: themeOptions,
			choice:  themeChoice,
		},
	}
	if hasApproval {
		items = append(items, approvalItem)
	}
	items = append(items, i.statusLineSettingsItems()...)
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
	// ctrlproto mode: the gate lives daemon-side; read the permissions
	// surface and feed the same renderer shapes.
	if c := i.cfg.Carrier; c != nil {
		sf, err := c.Surface(context.Background(), i.carrierSession(), "permissions")
		if err != nil || sf.Permissions == nil {
			msg := i18n.T("permissions surface unavailable")
			if err != nil {
				msg = err.Error()
			}
			return []string{th.FG256(th.Muted, msg)}, nil
		}
		return renderPermissionsWireView(th, *sf.Permissions)
	}
	gate := i.cfg.ConfirmGate
	if gate == nil {
		return []string{th.FG256(th.Muted, i18n.T("no permission gate (yolo): every tool runs without asking."))}, nil
	}

	var out []string
	out = append(out, th.FG256(th.Accent, tui.Bold(i18n.T("approval mode")))+"  "+string(gate.Mode()))
	out = append(out, th.FG256(th.Muted, "  "+i18n.T("change it in /settings; flags --approval / --no-yolo set the startup default.")))
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
		out = append(out, th.FG256(th.Muted, i18n.T("no permission rules in effect.")))
	} else {
		out = append(out, th.FG256(th.Accent, tui.Bold(i18n.T("rules")))+th.FG256(th.Muted, "  "+i18n.T("(first match wins; user → project → extension)")))
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

	out = append(out, th.FG256(th.Accent, tui.Bold(i18n.T("this session"))))
	allowAll, tools := gate.Grants()
	if allowAll {
		grants = append(grants, permGrant{allowAll: true})
	}
	for _, t := range tools {
		grants = append(grants, permGrant{tool: t})
	}
	if len(grants) == 0 {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("no session grants yet.")))
	}
	return out, grants
}

// approvalModeLabel returns the live approval mode for the status bar,
// or "" when there's no gate (so the bar falls back to its legacy
// NoYolo tag). "yolo" renders no badge.
func (i *Interactive) approvalModeLabel() string {
	if i.cfg.Carrier != nil {
		i.mu.Lock()
		defer i.mu.Unlock()
		return i.carrierApprovalMode
	}
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
	if i.cfg.Carrier == nil && (i.cfg.ConfirmGate == nil || i.cfg.SetApprovalMode == nil) {
		return settingsItem{}, false
	}
	opts := []settingsOption{
		{value: string(core.ApprovalPlan), label: i18n.T("plan"), desc: i18n.T("read-only tools only; mutating tools are withheld")},
		{value: string(core.ApprovalAsk), label: i18n.T("ask"), desc: i18n.T("confirm every tool call")},
		{value: string(core.ApprovalAutoEdit), label: i18n.T("auto-edit"), desc: i18n.T("run reads + file edits; confirm everything else")},
		{value: string(core.ApprovalWorkspace), label: i18n.T("workspace"), desc: i18n.T("run built-in tools + read-only tools; confirm side-effecting extension/MCP tools")},
		{value: string(core.ApprovalYolo), label: i18n.T("yolo"), desc: i18n.T("run every tool without asking")},
	}
	cur := ""
	if i.cfg.Carrier != nil {
		// ctrlproto mode: the gate lives daemon-side; the settings surface
		// carries the live per-session mode.
		sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "settings")
		if err != nil || sf.Settings == nil {
			return settingsItem{}, false
		}
		for _, it := range sf.Settings.Items {
			if it.Key == "approval" {
				cur = it.Value
				break
			}
		}
		if cur == "" {
			return settingsItem{}, false
		}
	} else {
		cur = string(i.cfg.ConfirmGate.Mode())
	}
	choice := 0
	for idx, o := range opts {
		if o.value == cur {
			choice = idx
			break
		}
	}
	return settingsItem{
		key:     "approval_mode",
		label:   i18n.T("approval mode"),
		desc:    i18n.T("how much the agent may do without asking (see /permissions)"),
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
	case "status_line":
		i.applyStatusLinePreset(act.StringValue)
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
	if err != nil {
		return
	}
	// ctrlproto mode: the settings surface flips the daemon-side gate live
	// (per-session, not persisted — same posture semantics) and rebuilds the
	// session's tool set in the new mode, so plan withholds mutating tools
	// from the model's view exactly like the legacy callback below.
	if c := i.cfg.Carrier; c != nil {
		if aerr := c.SurfaceAction(context.Background(), i.carrierSession(), "settings", "set",
			map[string]string{"key": "approval", "value": value}); aerr != nil {
			i.setStatusErr(aerr.Error())
			return
		}
		i.mu.Lock()
		i.statusOK = i18n.T("approval mode %s (this session)", value)
		i.statusErr = ""
		i.mu.Unlock()
		return
	}
	if i.cfg.SetApprovalMode == nil {
		return
	}
	reg := i.cfg.SetApprovalMode(mode)
	i.mu.Lock()
	if ag := i.turns.Agent(); ag != nil && reg != nil {
		ag.SetTools(reg)
	}
	i.statusOK = i18n.T("approval mode %s (this session)", value)
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
	if i.dispatchStatusSegmentToggle(key, value) {
		return
	}
	switch key {
	case "inline_images_enabled":
		val := value
		i.cfg.InlineImagesEnabled = &val
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetInlineImages(value); err != nil {
				i.mu.Lock()
				i.statusErr = i18n.T("settings: %s", err)
				i.mu.Unlock()
				return
			}
		}
		i.mu.Lock()
		i.view.ImageProto = effectiveImageProtocol(i.cfg.InlineImagesEnabled)
		i.view.InvalidateRenderCache()
		i.statusOK = i18n.T("inline image rendering %s", onOff(value))
		i.statusErr = ""
		i.mu.Unlock()
	case "auto_swarm_enabled":
		val := value
		i.cfg.AutoSwarmEnabled = &val
		// Carrier path: the daemon owns persistence AND the live apply (it
		// rebuilds every session's tool set + system prompt); the local
		// pointer above just keeps this dialog's checkbox current.
		if c := i.cfg.Carrier; c != nil {
			if err := c.SurfaceAction(context.Background(), i.carrierSession(), "settings", "set",
				map[string]string{"key": "auto_swarm", "value": strconv.FormatBool(value)}); err != nil {
				i.mu.Lock()
				i.statusErr = i18n.T("settings: %s", err)
				i.mu.Unlock()
				return
			}
			i.mu.Lock()
			i.statusOK = i18n.T("auto-swarm %s", onOff(value))
			i.statusErr = ""
			i.mu.Unlock()
			return
		}
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetAutoSwarm(value); err != nil {
				i.mu.Lock()
				i.statusErr = i18n.T("settings: %s", err)
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
		i.statusOK = i18n.T("auto-swarm %s", onOff(value))
		i.statusErr = ""
		i.mu.Unlock()
	case "recursive_file_suggest":
		val := value
		i.cfg.RecursiveFileSuggest = &val
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetRecursiveFileSuggest(value); err != nil {
				i.mu.Lock()
				i.statusErr = i18n.T("settings: %s", err)
				i.mu.Unlock()
				return
			}
		}
		// Flip the live picker so the next @ reflects the new mode
		// without restarting terva. SetRecursive drops its cache.
		i.fileSuggest.SetRecursive(value)
		i.mu.Lock()
		i.statusOK = i18n.T("recursive @-file search %s", onOff(value))
		i.statusErr = ""
		i.mu.Unlock()
	case "respect_gitignore":
		val := value
		i.cfg.RespectGitignore = &val
		if i.cfg.SettingsStore != nil {
			if err := i.cfg.SettingsStore.SetRespectGitignore(value); err != nil {
				i.mu.Lock()
				i.statusErr = i18n.T("settings: %s", err)
				i.mu.Unlock()
				return
			}
		}
		i.fileSuggest.SetRespectGitignore(value)
		i.mu.Lock()
		i.statusOK = i18n.T("hide gitignored files in @-picker %s", onOff(value))
		i.statusErr = ""
		i.mu.Unlock()
	}
}

func (i *Interactive) applyThemeSetting(name string) {
	if i.cfg.SettingsStore != nil {
		if err := i.cfg.SettingsStore.SetTheme(name); err != nil {
			i.mu.Lock()
			i.statusErr = i18n.T("settings: %s", err)
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
		i.statusErr = i18n.T("theme missing; reset to default")
		i.mu.Unlock()
	} else {
		i.mu.Lock()
		label := applied
		if label == "" {
			label = "auto"
		}
		i.statusOK = i18n.T("theme %s", label)
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
			i.statusErr = i18n.T("settings: %s", err)
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
	i.statusOK = i18n.T("thinking level %s", label)
	i.statusErr = ""
	i.mu.Unlock()
}
