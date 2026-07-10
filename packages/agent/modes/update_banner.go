package modes

import (
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// UpdateInfo mirrors agent.UpdateInfo without the import cycle. The
// parent package builds one of these via agent.CheckForUpdate and
// passes it in through InteractiveConfig.UpdateInfoChan.
type UpdateInfo struct {
	Current   string
	Latest    string
	Available bool
	URL       string
}

// renderUpdateBanner builds the "new version available" block shown at
// the top of the chat area. Yellow-framed like a warning, but worded
// gently since this is informational, not urgent.
//
// Returns nil when no update is available, so callers can just
// append (or prepend) unconditionally.
func renderUpdateBanner(th tui.Theme, info UpdateInfo, width int) []string {
	if !info.Available {
		return nil
	}
	color := th.Warning
	out := []string{
		dialogs.FrameHeaderColor(th, "update available", width, color),
	}
	out = append(out, "")

	title := i18n.T("terva %s is available (you're on %s).", info.Latest, info.Current)
	out = append(out, "  "+th.FG256(color, tui.Bold(title)))
	out = append(out, "")
	out = append(out, "  "+th.FG256(th.Muted, i18n.T("run: "))+th.FG256(color, "terva update"))

	if info.URL != "" {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("changelog: "))+th.FG256(color, info.URL))
	}

	out = append(out, "")
	out = append(out, dialogs.FrameRuleColor(th, width, color))
	out = append(out, "")
	return out
}
