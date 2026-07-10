package mcp

// Info is the MCP-server view: config-derived enablement plus ground truth from
// the live Manager. It lives here, not in the TUI, for the same reason as
// extensions.Info.

// Info is one configured MCP server's display + state for the /mcp
// dialog. The host (cli) scans the user + trusted-project mcp.servers
// config, overlays the live Manager's connection/tool state, and resolves
// the disable flags; this package only renders it and emits toggle
// actions. Unlike extensions there is no per-server manifest — "enabled"
// means the name is absent from disable_mcp.
//
// Two independent controls, mirroring /extensions:
//
//   - 'g' (global): adds/removes the server from the USER config's
//     disable_mcp. Only meaningful for a user-defined server (Scope
//     "global"); a project-defined server has nothing to toggle here.
//   - 'p' (project): adds/removes the server from THIS project's
//     disable_mcp. Restrict-only — it can disable a user server here, but
//     can never enable one the user disabled.
type Info struct {
	Name        string
	Scope       string // "global" (user-defined) | "project"
	Description string // serverInfo name or the command, for the detail line

	UserDisabled    bool
	ProjectDisabled bool
	ProjectGated    bool // project-defined server in an untrusted workspace
	Effective       bool // config says it should run

	// Connected and Tools are ground truth from the live Manager.
	// Connected can be false while Effective is true — a server that
	// failed to spawn (see StartupError).
	Connected bool
	Tools     int

	StartupError string // from Manager.Warnings(), if it failed to start
	HasLog       bool
}
