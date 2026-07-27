package workspace

import (
	"encoding/json"

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
			Config:             extensionConfigForm(s, e),
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
	case "set_config":
		// Persist a submitted form, then apply it — the same two steps as
		// toggle, in the same order, from any client.
		//
		// This goes THROUGH the running daemon on purpose. config.json is
		// rewritten by the live instance, so anything that writes the file
		// behind it loses the change at the next rewrite; that is why the
		// browser could always apply a config it had no way to author, and why
		// a file-writing CLI would need the service stopped. The one process
		// that owns the file does the write.
		var values map[string]string
		if raw := args["values"]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &values); err != nil {
				return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "set_config: %v", err)
			}
		}
		// Typed here, against the schema, rather than by the client: three
		// clients deciding independently whether a field is a bool is three
		// chances to write a config the extension cannot read.
		if err := build.SetExtensionConfigForm(s.extMgr, s.cwd, name, values); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "set_config %s: %v", name, err)
		}
		build.ApplyExtensionConfigLive(s.extMgr, s.cwd, name)
		s.broadcast(ctrlproto.SurfaceUpdatedEvent("extensions"))
		return nil
	case "clear_config_key":
		// Delete ONE saved value, then apply — the operation a submitted form
		// cannot express, because a blank secret already means "keep what is
		// stored" (see build.ClearExtensionConfigKey). Same two steps, same
		// order, and the same reason it lives daemon-side: the process that
		// owns config.json does the write.
		key := args["key"]
		if key == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("extensions: missing key"))
		}
		if err := build.ClearExtensionConfigKey(name, key); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "clear_config_key %s.%s: %v", name, key, err)
		}
		build.ApplyExtensionConfigLive(s.extMgr, s.cwd, name)
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

// extensionConfigForm projects one extension's declared schema and saved values
// onto the wire.
//
// HasConfig and this must agree. A flag advertising a form a client cannot
// fetch is worse than no flag: the browser showed a settings affordance backed
// by nothing, because the schema was a local-disk read and the browser has no
// disk. So the form is built here, once, on the host, and every client renders
// what it is handed.
//
// The directory comes from the LIVE extension where there is one, which is what
// makes this work for a --ext load: those live outside every install root, so
// resolving by name finds nothing and reports, truthfully and uselessly, that
// an extension with a schema has no settings.
func extensionConfigForm(s *wsSession, e extensions.Info) []ctrlproto.ExtensionConfigField {
	if !e.HasConfig {
		return nil
	}
	fields := build.ExtensionConfigFormIn(e.Dir, e.Name)
	if len(fields) == 0 {
		fields = build.ExtensionConfigForm(s.extMgr, s.cwd, e.Name)
	}
	out := make([]ctrlproto.ExtensionConfigField, 0, len(fields))
	for _, f := range fields {
		out = append(out, ctrlproto.ExtensionConfigField{
			Key:         f.Key,
			Label:       f.Label,
			Type:        f.Type,
			Description: f.Description,
			Required:    f.Required,
			Secret:      f.Secret,
			Options:     f.Options,
			// Saved is already empty for a secret (build.ExtensionConfigForm
			// refuses to populate it); copying it unconditionally is safe and
			// keeps the rule in ONE place rather than restating it here, where
			// a later edit could quietly disagree with it.
			Saved:    f.Saved,
			Default:  f.Default,
			HasSaved: f.HasSaved,
		})
	}
	return out
}
