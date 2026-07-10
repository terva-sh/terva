package modes

import (
	"context"
	"time"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
)

// ExtensionHost is the interactive TUI's view of the extension subsystem: the
// slash-command catalog + dispatch, status-bar segments, injected-context
// snapshot, panel key routing, and live reload. It exists so the TUI is
// extension-transport-agnostic.
//
// *extensions.Manager satisfies it directly — the legacy path hands the TUI its
// one process-wide manager. The default (carrier) path supplies an adapter that
// resolves the CURRENT workspace session's per-session manager on each call, so
// a session switch transparently re-targets the live extension set and the TUI
// never holds a session-scoped pointer. A future remote carrier swaps in a
// wire-backed impl; the TUI code doesn't change.
type ExtensionHost interface {
	// HasCommand / CommandOwner / Commands back the slash prompt: dispatch
	// routing, the "[ext]" attribution, and command autocomplete.
	HasCommand(name string) bool
	CommandOwner(name string) string
	Commands() []extdriver.CommandInfo
	// Invoke runs an extension command; the TUI applies the returned action
	// (open_panel / prompt / insert / display / noop) locally.
	Invoke(ctx context.Context, name, args string, timeout time.Duration) (extproto.CommandResponseFromExt, error)
	// StatusSegments feeds the status bar; ContextSnapshot feeds /context.
	StatusSegments() []string
	ContextSnapshot() []extdriver.ContextItem
	// SendPanelKey / SendPanelClose route input for an open extension panel.
	SendPanelKey(extName, panelID, key, text string) error
	SendPanelClose(extName, panelID string) error
	// Reload re-scans + respawns extensions (/reload-ext).
	Reload(ctx context.Context, grace time.Duration) extensions.ReloadStats
	// SetProjectTrusted flips the project-extension trust gate on a live /trust.
	SetProjectTrusted(trusted bool)
}

// *extensions.Manager is the legacy-path ExtensionHost (methods promoted from
// its embedded *extdriver.Driver, plus Reload / SetProjectTrusted).
var _ ExtensionHost = (*extensions.Manager)(nil)
