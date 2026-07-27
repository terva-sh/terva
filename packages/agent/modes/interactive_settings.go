package modes

// The /settings and /permissions surfaces: item builders, dialog
// plumbing, and the appliers each toggle dispatches to.

import (
	"context"
	"strconv"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
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

func onOff(v bool) string {
	if v {
		return i18n.T("enabled")
	}
	return i18n.T("disabled")
}

func (i *Interactive) openSettingsDialog() {
	items := i.daemonSettingsItems()
	// TUI-local widgets the generic surface can't drive: the theme picker needs
	// on-disk + extension theme discovery the daemon's fixed enum lacks, and the
	// status-line layout is a terminal-only concern.
	items = append(items, i.themeSettingsItem())
	items = append(items, i.statusLineSettingsItems()...)
	i.settingsDialog.Open(items)
}

// daemonSettingsItems renders the daemon settings surface — the single source
// of truth the web renders too — into dialog rows. Every config setting flows
// through here, so a new item in workspace_settings.go appears in the TUI with
// no change here. "theme" is dropped: the TUI shows its own richer picker.
func (i *Interactive) daemonSettingsItems() []dialogs.SettingsItem {
	if i.cfg.Carrier == nil {
		return nil // no workspace bound: the daemon owns settings
	}
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "settings")
	if err != nil || sf.Settings == nil {
		return nil
	}
	items := make([]dialogs.SettingsItem, 0, len(sf.Settings.Items))
	for _, it := range sf.Settings.Items {
		if it.Key == "theme" {
			continue // rendered locally by themeSettingsItem (theme discovery)
		}
		items = append(items, wireToSettingsItem(it))
	}
	return items
}

// wireToSettingsItem maps one settings-surface item to a dialog row: an enum
// becomes an option picker (Choice tracks the current value), anything else a
// checkbox. The item's Note rides along as the row hint.
func wireToSettingsItem(it ctrlproto.SettingItem) dialogs.SettingsItem {
	si := dialogs.SettingsItem{
		Key:   it.Key,
		Label: it.Label,
		Desc:  it.Description,
		Hint:  it.Note,
	}
	if it.Type == "enum" {
		si.Options = make([]dialogs.SettingsOption, len(it.Options))
		for idx, o := range it.Options {
			si.Options[idx] = dialogs.SettingsOption{Value: o.Value, Label: o.Label}
			if o.Value == it.Value {
				si.Choice = idx
			}
		}
		return si
	}
	si.Value = it.Value == "true"
	return si
}

// themeSettingsItem builds the TUI-local theme picker from the on-disk +
// extension theme catalog — discovery the daemon's fixed theme enum can't see.
// It also self-heals a stale/removed theme name back to "auto".
func (i *Interactive) themeSettingsItem() dialogs.SettingsItem {
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
	availableThemes := tui.AvailableThemes(i.cfg.TervaHome)
	if i.cfg.ExtensionThemes != nil {
		availableThemes = append(availableThemes, i.cfg.ExtensionThemes()...)
	}
	options := make([]dialogs.SettingsOption, 0, len(availableThemes))
	choice := 0
	for idx, opt := range availableThemes {
		options = append(options, dialogs.SettingsOption{Value: opt.Value, Label: opt.Label, Desc: opt.Description})
		if opt.Value == themeName {
			choice = idx
		}
	}
	return dialogs.SettingsItem{
		Key:     "theme",
		Label:   i18n.T("color theme"),
		Desc:    i18n.T("choose a theme from $TERVA_HOME/themes or a loaded extension"),
		Options: options,
		Choice:  choice,
	}
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

// applySettingChange routes a dialog action to its applier. The status-line
// segment toggles, the status-line preset, and the theme picker are TUI-local
// widgets (a terminal-only layout, dynamic theme discovery); every other item
// is a generic view of the daemon settings surface, so its change goes through
// the one daemon action that persists + applies it live — the same path the
// web uses.
func (i *Interactive) applySettingChange(act dialogs.SettingsAction) {
	// Status-line segment show/hide (statusseg_*) is a TUI-local widget.
	if i.dispatchStatusSegmentToggle(act.Key, act.Value) {
		return
	}
	switch act.Key {
	case "theme":
		i.applyThemeSetting(act.StringValue)
	case "status_line":
		i.applyStatusLinePreset(act.StringValue)
	default:
		i.applyDaemonSetting(act)
	}
}

// applyDaemonSetting sends a generic settings-surface change to the daemon —
// the single persist + live-apply path shared with the web — then runs any
// TUI-process-local live effect (image protocol, the @-file picker, the
// reasoning label) that a config write alone can't reach in the running UI.
// Enforcement-shaping settings (approval) and every persist-only preference
// are handled entirely daemon-side; the reopened dialog re-reads the surface,
// so nothing local is needed for those.
func (i *Interactive) applyDaemonSetting(act dialogs.SettingsAction) {
	// Force a full repaint at the end (same as Ctrl+L) so any per-setting
	// visual change lands immediately instead of on the next diff frame.
	defer func() {
		if i.rend != nil {
			i.rend.Clear()
		}
		i.invalidate()
	}()
	if i.cfg.Carrier == nil {
		return // no workspace bound: the daemon owns settings
	}
	value := strconv.FormatBool(act.Value)
	if act.Enum {
		value = act.StringValue // includes the empty value (e.g. reasoning "off")
	}
	if err := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "settings", "set",
		map[string]string{"key": act.Key, "value": value}); err != nil {
		i.setStatusErr(i18n.T("settings: %s", err.Error()))
		return
	}
	i.applyLocalSettingEffect(act.Key, value)
}

// applyLocalSettingEffect refreshes this process's own state after the daemon
// accepted and persisted a change. Most settings are resolved at session build
// or applied live daemon-side (nothing local to do); a few reach into the TUI's
// view (image rendering), the live @-file picker, or the dialog's local label.
func (i *Interactive) applyLocalSettingEffect(key, value string) {
	on := value == "true"
	switch key {
	case "inline_images":
		v := on
		i.cfg.InlineImagesEnabled = &v
		i.mu.Lock()
		i.view.ImageProto = effectiveImageProtocol(i.cfg.InlineImagesEnabled)
		i.view.InvalidateRenderCache()
		i.statusOK = i18n.T("inline image rendering %s", onOff(on))
		i.statusErr = ""
		i.mu.Unlock()
	case "recursive_file_suggest":
		v := on
		i.cfg.RecursiveFileSuggest = &v
		// Flip the live picker so the next @ reflects the new mode without a
		// restart; SetRecursive drops its cache.
		i.fileSuggest.SetRecursive(on)
		i.setStatusOK(i18n.T("recursive @-file search %s", onOff(on)))
	case "respect_gitignore":
		v := on
		i.cfg.RespectGitignore = &v
		i.fileSuggest.SetRespectGitignore(on)
		i.setStatusOK(i18n.T("hide gitignored files in @-picker %s", onOff(on)))
	case "reasoning":
		label := value
		if label == "" {
			label = "off"
		}
		i.mu.Lock()
		i.cfg.Reasoning = value
		i.statusOK = i18n.T("thinking level %s", label)
		i.statusErr = ""
		i.mu.Unlock()
	case "reasoning_summary":
		// Nothing local to cache: unlike `reasoning` there is no TUI label
		// reading it back, and the daemon applies it live to every agent.
		// Confirm the change so an enum landing on "off" still reads as an
		// action rather than a silent no-op.
		label := value
		if label == "" {
			label = "off"
		}
		i.setStatusOK(i18n.T("recorded thinking %s", label))
	case "approval":
		// Per-session posture (never persisted); shift+tab and the picker share it.
		i.setStatusOK(i18n.T("approval mode %s (this session)", value))
	case "trust":
		// The daemon has already persisted the verdict and re-applied it across
		// every session. What is local is the TUI's own copy: the /status line,
		// and the untrusted reminder that would otherwise keep nagging about a
		// workspace the user just trusted. Same state /trust and /untrust set,
		// reached from the settings pane instead.
		i.mu.Lock()
		i.cfg.Trusted = on
		cwd := i.cfg.CWD
		i.mu.Unlock()
		if i.cfg.Extensions != nil {
			i.cfg.Extensions.SetProjectTrusted(on)
		}
		if on {
			i.setStatusOK(i18n.T("trusted %s — its project extensions, skills, and context are loaded now", cwd))
		} else {
			i.setStatusOK(i18n.T("untrusted %s — its project extensions, skills, and context are unloaded", cwd))
		}
	default:
		i.setStatusOK(i18n.T("setting updated"))
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
		th, _, _ = tui.LoadThemeFromHome(i.cfg.TervaHome, "auto", detected)
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

// applyApprovalMode switches the approval posture for THIS SESSION through the
// generic daemon path: settingsAction("set", approval) flips the daemon-side
// gate live and rebuilds the session's tool set so plan withholds mutating
// tools from the model's view. Deliberately NOT persisted — approval is a
// security posture like /jail, not a preference; the persistent default comes
// only from an explicit `approval` config key or the --approval flag. Shared by
// the /settings picker (via applySettingChange) and the shift+tab wheel.
func (i *Interactive) applyApprovalMode(value string) {
	i.applyDaemonSetting(dialogs.SettingsAction{Enum: true, Key: "approval", StringValue: value})
}
