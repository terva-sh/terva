package modes

import (
	"fmt"

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
		frameHeaderColor(th, "update available", width, color),
	}
	out = append(out, "")

	title := fmt.Sprintf("terva %s is available (you're on %s).", info.Latest, info.Current)
	out = append(out, "  "+th.FG256(color, tui.Bold(title)))
	out = append(out, "")
	out = append(out, "  "+th.FG256(th.Muted, "run: ")+th.FG256(color, "terva update"))

	if info.URL != "" {
		out = append(out, "  "+th.FG256(th.Muted, "changelog: ")+th.FG256(color, info.URL))
	}

	out = append(out, "")
	out = append(out, frameRuleColor(th, width, color))
	out = append(out, "")
	return out
}
