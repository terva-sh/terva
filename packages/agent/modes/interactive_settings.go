package modes

// The /settings and /permissions surfaces: item builders, dialog
// plumbing, and the appliers each toggle dispatches to.

import (
	"context"
	"strconv"

	"terva.sh/terva/packages/agent/modes/dialogs"
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

	reasoningOptions := []dialogs.SettingsOption{
		{Value: "", Label: i18n.T("off"), Desc: i18n.T("no reasoning")},
		{Value: "minimum", Label: i18n.T("minimum"), Desc: i18n.T("very brief (~1k tokens)")},
		{Value: "low", Label: i18n.T("low"), Desc: i18n.T("light (~2k tokens)")},
		{Value: "medium", Label: i18n.T("medium"), Desc: i18n.T("moderate (~8k tokens)")},
		{Value: "high", Label: i18n.T("high"), Desc: i18n.T("deep (~16k tokens)")},
		{Value: "maximum", Label: i18n.T("maximum"), Desc: i18n.T("highest (~32k tokens)")},
	}
	reasoning := provider.NormalizeReasoning(i.cfg.Reasoning)
	reasoningChoice := 0
	for idx, opt := range reasoningOptions {
		if opt.Value == reasoning {
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
	themeOptions := []dialogs.SettingsOption{}
	themeChoice := 0
	availableThemes := tui.AvailableThemes(i.cfg.TervaHome)
	if i.cfg.ExtensionThemes != nil {
		availableThemes = append(availableThemes, i.cfg.ExtensionThemes()...)
	}
	for idx, opt := range availableThemes {
		themeOptions = append(themeOptions, dialogs.SettingsOption{Value: opt.Value, Label: opt.Label, Desc: opt.Description})
		if opt.Value == themeName {
			themeChoice = idx
		}
	}

	approvalItem, hasApproval := i.approvalSettingItem()

	items := []dialogs.SettingsItem{
		{
			Key:      "inline_images_enabled",
			Label:    i18n.T("render images when supported"),
			Desc:     i18n.T("draw screenshots inline instead of showing a text placeholder"),
			Value:    imgEnabled,
			Disabled: imgDisabled,
			Hint:     imgHint,
		},
		{
			Key:      "auto_swarm_enabled",
			Label:    i18n.T("auto-swarm"),
			Desc:     i18n.T("let the agent spawn background sub-agents in parallel via the swarm_spawn tool"),
			Value:    autoSwarm,
			Disabled: autoSwarmDisabled,
			Hint:     autoSwarmHint,
		},
		{
			Key:   "recursive_file_suggest",
			Label: i18n.T("recursive @-file search"),
			Desc:  i18n.T("fuzzy-search the whole project tree when picking files with @ instead of browsing one directory at a time"),
			Value: recursiveFiles,
		},
		{
			Key:   "respect_gitignore",
			Label: i18n.T("hide gitignored files in @-picker"),
			Desc:  i18n.T("skip files and directories matched by the project's root .gitignore (and .git) when picking files with @"),
			Value: respectGitignore,
		},
		{
			Key:     "reasoning",
			Label:   i18n.T("thinking level"),
			Desc:    i18n.T("reasoning depth for thinking-capable models"),
			Options: reasoningOptions,
			Choice:  reasoningChoice,
			Hint:    reasoningHint,
		},
		{
			Key:     "theme",
			Label:   i18n.T("color theme"),
			Desc:    i18n.T("choose a theme from $TERVA_HOME/themes or a loaded extension"),
			Options: themeOptions,
			Choice:  themeChoice,
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

// buildPermissionsView formats the daemon-side gate's mode and rules into
// theme-colored static lines (info) and returns this session's "always allow"
// grants as a selectable list. Reads the permissions surface, so it always
// reflects the current state.
func (i *Interactive) buildPermissionsView() (info []string, grants []dialogs.PermGrant) {
	th := i.cfg.Theme
	if i.cfg.Carrier == nil {
		// No workspace bound (an embedder/test). Degrade — never reach for a
		// local gate: the daemon owns permissions.
		return []string{th.FG256(th.Muted, i18n.T("permissions surface unavailable"))}, nil
	}
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "permissions")
	if err != nil || sf.Permissions == nil {
		msg := i18n.T("permissions surface unavailable")
		if err != nil {
			msg = err.Error()
		}
		return []string{th.FG256(th.Muted, msg)}, nil
	}
	return renderPermissionsWireView(th, *sf.Permissions)
}

// approvalModeLabel returns the live approval mode for the status bar. The gate
// lives daemon-side; refreshCarrierApprovalMode keeps this cache fed from the
// settings surface.
func (i *Interactive) approvalModeLabel() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.carrierApprovalMode
}

// approvalSettingItem builds the /settings approval-mode picker from the
// daemon-side gate, or reports false when the settings surface can't be read.
// The choice comes off the surface so reopening /settings always reflects the
// current mode.
func (i *Interactive) approvalSettingItem() (dialogs.SettingsItem, bool) {
	if i.cfg.Carrier == nil {
		return dialogs.SettingsItem{}, false // no workspace bound: no picker
	}
	opts := []dialogs.SettingsOption{
		{Value: string(core.ApprovalPlan), Label: i18n.T("plan"), Desc: i18n.T("read-only tools only; mutating tools are withheld")},
		{Value: string(core.ApprovalAsk), Label: i18n.T("ask"), Desc: i18n.T("confirm every tool call")},
		{Value: string(core.ApprovalAutoEdit), Label: i18n.T("auto-edit"), Desc: i18n.T("run reads + file edits; confirm everything else")},
		{Value: string(core.ApprovalWorkspace), Label: i18n.T("workspace"), Desc: i18n.T("run built-in tools + read-only tools; confirm side-effecting extension/MCP tools")},
		{Value: string(core.ApprovalYolo), Label: i18n.T("yolo"), Desc: i18n.T("run every tool without asking")},
	}
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "settings")
	if err != nil || sf.Settings == nil {
		return dialogs.SettingsItem{}, false
	}
	cur := ""
	for _, it := range sf.Settings.Items {
		if it.Key == "approval" {
			cur = it.Value
			break
		}
	}
	if cur == "" {
		return dialogs.SettingsItem{}, false
	}
	choice := 0
	for idx, o := range opts {
		if o.Value == cur {
			choice = idx
			break
		}
	}
	return dialogs.SettingsItem{
		Key:     "approval_mode",
		Label:   i18n.T("approval mode"),
		Desc:    i18n.T("how much the agent may do without asking (see /permissions)"),
		Options: opts,
		Choice:  choice,
	}, true
}

func (i *Interactive) applySettingChange(act dialogs.SettingsAction) {
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

// applyApprovalModeSetting switches the approval mode for THIS SESSION: the
// settings surface flips the daemon-side gate live and rebuilds the session's
// tool set in the new mode, so plan withholds mutating tools from the model's
// view. Deliberately NOT persisted — the approval mode is a security posture
// like /jail, not a preference like theme. The persistent default comes only
// from an explicit `approval` key in config or the --approval flag, so the
// picker can never silently pin a mode (least of all the current default) into
// config and mask a future default change.
func (i *Interactive) applyApprovalModeSetting(value string) {
	defer i.invalidate()
	if i.cfg.Carrier == nil {
		return // no workspace bound: nothing to switch
	}
	if _, err := core.ParseApprovalMode(value); err != nil {
		return
	}
	if aerr := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "settings", "set",
		map[string]string{"key": "approval", "value": value}); aerr != nil {
		i.setStatusErr(aerr.Error())
		return
	}
	i.mu.Lock()
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
		// The daemon owns persistence AND the live apply: settingsAction
		// re-derives every session's tool set and system prompt from the new
		// config (rebuildTools). The local pointer above just keeps this
		// dialog's checkbox current. The TUI used to patch the running
		// agent's registry and System string itself, in parallel; that code
		// was unreachable behind this same carrier check.
		if i.cfg.Carrier == nil {
			return // no workspace bound: nothing to toggle
		}
		if err := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "settings", "set",
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
	if i.cfg.Carrier == nil {
		return // no workspace bound: nothing to apply the level to
	}
	// The daemon owns both halves. settingsAction("set", reasoning) persists
	// the level and applies it live to every session's agent, through the
	// locked Agent.SetReasoning. The TUI used to do both itself, and its
	// half of the second one was a bare `ag.Reasoning = level` — an
	// unsynchronized write to a field the turn loop reads under the agent's
	// own mutex.
	if err := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "settings", "set",
		map[string]string{"key": "reasoning", "value": level}); err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("settings: %s", err)
		i.mu.Unlock()
		return
	}
	label := level
	if label == "" {
		label = "off"
	}
	i.mu.Lock()
	i.cfg.Reasoning = level
	i.statusOK = i18n.T("thinking level %s", label)
	i.statusErr = ""
	i.mu.Unlock()
}
