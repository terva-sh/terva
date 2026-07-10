package modes

import (
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/modes/dialogs"
)

// Plain-data views that callers outside this package name directly:
// packages/agent reaches for modes.ConfigField, and the workspace tests
// for modes.ExtInfo / modes.MCPInfo. The dialogs package moved out from
// under them, so keep the short names here.
//
// These are aliases, not definitions. modes.ExtInfo and dialogs.ExtInfo
// are the same type as extensions.Info, so a []ExtInfo built here passes
// straight into a dialog without conversion.
//
// ExtInfo and MCPInfo alias the truth rather than dialogs' copy of it:
// they are extension and MCP status, not TUI shapes. That distinction is
// why A2 moved them out of this package in the first place -- the
// ctrlproto server had been building its wire view out of TUI-shaped
// DTOs. ConfigField still lives in the dialog that renders it and would
// likely belong in extensions for the same reason; that is a design
// change, not this refactor.
type (
	ExtInfo     = extensions.Info
	MCPInfo     = mcp.Info
	ConfigField = dialogs.ConfigField
)
