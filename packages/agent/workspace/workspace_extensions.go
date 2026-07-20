package workspace

import (
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/i18n"
)

// The extensions management surface: a read-only inventory + health rollup of
// the session's installed/loaded extensions, mirroring the TUI's extensions
// dialog (listInstalledExtensions → extensions.Info). Live enable/disable is a
// later slice — it needs the tool-registry rebuild (SetOnReload) wired into web
// sessions first, or the model's tool schema goes stale after a toggle.

// extensionsView builds the extensions pane from on-disk manifests merged with
// live ground truth from the driver (running state, tool/command counts).
func (s *wsSession) extensionsView() *ctrlproto.ExtensionsView {
	v := &ctrlproto.ExtensionsView{}
	if s.extMgr == nil {
		return v
	}
	for _, e := range build.ListInstalledExtensions(s.cwd, s.trusted.Load(), s.extMgr) {
		v.Extensions = append(v.Extensions, ctrlproto.ExtensionInfo{
			Name:               e.Name,
			Version:            e.Version,
			Language:           e.Language,
			Description:        e.Description,
			Scope:              e.Scope,
			Status:             extensionStatus(e),
			Enabled:            e.Effective,
			Tools:              e.Tools,
			Commands:           e.Commands,
			Note:               extensionNote(e),
			GlobalEnabled:      e.GlobalEnabled,
			ProjectDisabled:    e.ProjectDisabled,
			UserConfigDisabled: e.UserConfigDisabled,
			HasConfig:          e.HasConfig,
			HasLog:             e.HasLog,
		})
	}
	return v
}

// extensionsAction toggles an extension on/off for this workspace: it persists
// the choice to the project config's disable list, then applies it live
// (start/stop the subprocess + rebuild the agent's tool set via SetOnReload).
// Persisted, so it survives a restart; scoped to the pinned project.
func (s *wsSession) extensionsAction(action string, args map[string]string) error {
	name := args["name"]
	if name == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("extensions: missing name"))
	}
	switch action {
	case "toggle":
		on := args["enabled"] == "true"
		// scope "global" flips the extension's manifest (its install-wide
		// enabled flag, the TUI dialog's 'g' toggle); the default/"project"
		// scope keeps the original wire behavior — the project config's
		// disable list.
		if args["scope"] == "global" {
			dir, err := config.FindExtensionDirIn(s.cwd, name)
			if err != nil {
				return ctrlproto.Errorf(ctrlproto.CodeNotFound, "toggle %s: %v", name, err)
			}
			if err := config.SetManifestEnabled(dir, on); err != nil {
				return ctrlproto.Errorf(ctrlproto.CodeInternal, "toggle %s: %v", name, err)
			}
		} else if err := config.SetProjectExtensionDisabled(s.cwd, name, !on); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "toggle %s: %v", name, err)
		}
		// Reads the fresh disable union, then ApplyOne (start/stop) → onReload
		// rebuilds the model-facing tool set. Synchronous.
		build.ApplyExtensionChangeLive(s.extMgr, s.cwd, s.trusted.Load(), name)
		s.broadcast(ctrlproto.SurfaceUpdatedEvent("extensions"))
		return nil
	case "config":
		// Push an already-persisted config change to the running extension
		// (config_update) and restart/rebuild as needed — the live half of the
		// config form (the host persists the values; see setExtensionConfig-
		// FromForm). Same pattern as toggle: persist first, then apply live.
		build.ApplyExtensionConfigLive(s.extMgr, s.cwd, name)
		s.broadcast(ctrlproto.SurfaceUpdatedEvent("extensions"))
		return nil
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown extensions action %q", action))
	}
}

// extensionStatus collapses the ExtInfo flags into one badge value: running,
// disabled (config off), gated (project ext in an untrusted workspace), or
// stopped (enabled but not running — crashed or failed to spawn).
func extensionStatus(e extensions.Info) string {
	switch {
	case e.Running:
		return "running"
	case e.ProjectGated:
		return "gated"
	case !e.Effective:
		return "disabled"
	default:
		return "stopped"
	}
}

// extensionNote surfaces the crash/why-off reason (log tail) for a stopped ext.
func extensionNote(e extensions.Info) string {
	if !e.Running && e.LastLog != "" {
		return e.LastLog
	}
	return ""
}
