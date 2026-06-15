//go:build terva_acp

package acp

import (
	"encoding/json"
	"fmt"
)

// ---- session/new + session/load `mcpServers` (Phase 4a) ----
//
// ACP's McpServer is a tagged union discriminated by `type` (schema/v1):
//
//   - stdio (DEFAULT, no `type`): {name, command, args, env:[{name,value}]}
//   - http  ({type:"http",  name, url, headers})
//   - sse   ({type:"sse",   name, url, headers})
//
// terva's MCP is stdio-only (mcp.ServerConfig has no URL/transport), so we
// accept stdio entries and skip-with-a-warning any http/sse entry. The
// initialize handshake advertises mcpCapabilities {http:false, sse:false,
// acp:false}; a well-behaved client therefore never sends http/sse, but a
// misbehaving one is tolerated (skipped), not fatal (§10, §12, §13).

// MCPServerStdio is one stdio MCP server the editor asked terva to launch,
// in a transport-neutral shape the host converts to mcp.ServerConfig. Env is
// already flattened from the ACP [{name,value}] list to terva's name→value
// map. Kept in the acp package (not coupled to the mcp package) so the union
// parsing is unit-testable here and the host owns the mcp.Config build.
type MCPServerStdio struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// mcpServerEnvVar is one {name, value} entry in an stdio server's env array.
type mcpServerEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// mcpServerWire is the on-wire union shape. `type` discriminates: absent or
// "stdio" is the stdio variant; "http"/"sse" are the non-stdio transports.
// All fields are carried so a single decode covers every variant; only the
// stdio fields are consumed.
type mcpServerWire struct {
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     []mcpServerEnvVar `json:"env,omitempty"`
	// URL/Headers exist only on http/sse; decoded so the warning can name the
	// server, never used for spawning.
	URL string `json:"url,omitempty"`
}

// ParseMCPServers decodes the raw session/new / session/load `mcpServers`
// array into the stdio servers terva can launch, returning a warning per
// skipped (http/sse) entry so the host can surface what was ignored. A nil /
// empty / absent array yields no servers and no warnings. A malformed array
// yields a single warning and no servers (one bad payload must not fail the
// session — MCP is best-effort, like setupMCP).
func ParseMCPServers(raw json.RawMessage) (servers []MCPServerStdio, warnings []string) {
	if len(raw) == 0 {
		return nil, nil
	}
	var entries []mcpServerWire
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, []string{fmt.Sprintf("mcpServers ignored: malformed payload: %v", err)}
	}
	for i, e := range entries {
		switch e.Type {
		case "", "stdio":
			name := e.Name
			if name == "" {
				warnings = append(warnings, fmt.Sprintf("mcp server #%d skipped: missing name", i))
				continue
			}
			if e.Command == "" {
				warnings = append(warnings, fmt.Sprintf("mcp server %q skipped: missing command", name))
				continue
			}
			var env map[string]string
			if len(e.Env) > 0 {
				env = make(map[string]string, len(e.Env))
				for _, kv := range e.Env {
					if kv.Name == "" {
						continue
					}
					env[kv.Name] = kv.Value
				}
			}
			servers = append(servers, MCPServerStdio{
				Name:    name,
				Command: e.Command,
				Args:    e.Args,
				Env:     env,
			})
		default:
			// http / sse: terva MCP is stdio-only and these transports are
			// advertised off (mcpCapabilities). Skip with a warning rather
			// than fail — a conformant client never sends them.
			label := e.Name
			if label == "" {
				label = fmt.Sprintf("#%d", i)
			}
			warnings = append(warnings, fmt.Sprintf("mcp server %s skipped: %s transport not supported (terva MCP is stdio-only)", label, e.Type))
		}
	}
	return servers, warnings
}
