package workspace

import (
	"context"
	"errors"
	"time"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
)

// CarrierExtHost adapts the in-process Workspace's per-session extension
// manager to modes.ExtensionHost so the carrier TUI drives extension
// slash-commands, status segments, /context injection, panel input, and
// /reload-ext through whichever session it is currently bound to.
//
// Every method resolves the CURRENT session's manager (nil-safe pre-login and
// mid-switch), so a session switch transparently re-targets the live extension
// set — the TUI never holds a session-scoped pointer. extMgr is set once in
// buildSession and never reassigned, so reading it after existing() releases
// the workspace lock is safe; the manager's own methods are concurrency-safe.
// This is the transitional in-process seam the remote-carrier milestone later
// swaps for a wire-backed impl.
type CarrierExtHost struct {
	w    *Workspace
	sess func() string
}

// errNoSessionExtensions is returned by Invoke when there is no live session
// (a command can't run before login) — the other reads degrade to a zero value.
var errNoSessionExtensions = errors.New("no active session")

func (c CarrierExtHost) mgr() *extensions.Manager {
	if c.w == nil || c.sess == nil {
		return nil
	}
	s := c.w.existing(c.sess())
	if s == nil {
		return nil
	}
	return s.extMgr
}

func (c CarrierExtHost) HasCommand(name string) bool {
	if m := c.mgr(); m != nil {
		return m.HasCommand(name)
	}
	return false
}

func (c CarrierExtHost) CommandOwner(name string) string {
	if m := c.mgr(); m != nil {
		return m.CommandOwner(name)
	}
	return ""
}

func (c CarrierExtHost) Commands() []extdriver.CommandInfo {
	if m := c.mgr(); m != nil {
		return m.Commands()
	}
	return nil
}

func (c CarrierExtHost) Invoke(ctx context.Context, name, args string, timeout time.Duration) (extproto.CommandResponseFromExt, error) {
	if m := c.mgr(); m != nil {
		return m.Invoke(ctx, name, args, timeout)
	}
	return extproto.CommandResponseFromExt{}, errNoSessionExtensions
}

func (c CarrierExtHost) StatusSegments() []string {
	if m := c.mgr(); m != nil {
		return m.StatusSegments()
	}
	return nil
}

func (c CarrierExtHost) ContextSnapshot() []extdriver.ContextItem {
	if m := c.mgr(); m != nil {
		return m.ContextSnapshot()
	}
	return nil
}

func (c CarrierExtHost) SendPanelKey(extName, panelID, key, text string) error {
	if m := c.mgr(); m != nil {
		return m.SendPanelKey(extName, panelID, key, text)
	}
	return nil
}

func (c CarrierExtHost) SendPanelClose(extName, panelID string) error {
	if m := c.mgr(); m != nil {
		return m.SendPanelClose(extName, panelID)
	}
	return nil
}

func (c CarrierExtHost) Reload(ctx context.Context, grace time.Duration) extensions.ReloadStats {
	if m := c.mgr(); m != nil {
		return m.Reload(ctx, grace)
	}
	return extensions.ReloadStats{}
}

func (c CarrierExtHost) SetProjectTrusted(trusted bool) {
	if m := c.mgr(); m != nil {
		m.SetProjectTrusted(trusted)
	}
}

// NewCarrierExtHost binds an extension host to a workspace and a late-resolved
// session id. sess is a func because the carrier TUI learns its session only
// after the workspace is running.
func NewCarrierExtHost(w *Workspace, sess func() string) CarrierExtHost {
	return CarrierExtHost{w: w, sess: sess}
}
