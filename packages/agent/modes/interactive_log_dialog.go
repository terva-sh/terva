package modes

import "terva.sh/terva/packages/i18n"

// openLogDialog reads an extension's ("ext") or MCP server's ("mcp") log
// tail via the host callback and shows it in the scrollable viewer. Opened
// with 'l' from /extensions and /mcp.
func (i *Interactive) openLogDialog(kind, name string) {
	if i.logDialog == nil || i.cfg.ReadLogTail == nil {
		i.setStatusErr(i18n.T("log viewing is not available in this build"))
		return
	}
	lines := i.cfg.ReadLogTail(kind, name)
	if len(lines) == 0 {
		i.setStatusOK("no log for " + name + " yet")
		return
	}
	label := "log · " + name
	if kind == "mcp" {
		label = "mcp log · " + name
	}
	i.logDialog.Open(label, lines)
	i.invalidate()
}
