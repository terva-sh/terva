package agent

// Backs the interactive /mcp dialog (modes.mcpDialog): the configured
// server scan with live state, the two persistence surfaces it toggles
// (user disable_mcp and project disable_mcp), and a live start/stop.
// Plumbed into modes as InteractiveConfig callbacks so that package never
// imports this one — the same shape as the /extensions plumbing in
// extdialog.go. Unlike extensions there is no per-server manifest:
// "enabled" simply means the name is absent from disable_mcp.
