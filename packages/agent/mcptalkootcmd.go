package agent

import (
	"context"
	"flag"
	"os"
	"slices"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
)

// runMCPTalkootBridgeCommand handles `terva mcp-talkoot-bridge --socket <path>`,
// the stdio MCP server that gives an external Talkoot member its seat tools
// (decision 0023). It lists talkoot.BridgeTools with the text a native member
// reads, and relays each call over the unix socket at --socket, which the
// member's runner opened with 0600 permissions. The socket names the member.
//
// It is a companion binary path like mcp-approval-bridge: no config, no
// credentials, no home. Returns handled=false when rawArgs is not this command.
func runMCPTalkootBridgeCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "mcp-talkoot-bridge" {
		return false, nil
	}
	fs := flag.NewFlagSet("mcp-talkoot-bridge", flag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket path of the talkoot member's endpoint in the orchestrating terva")
	if perr := fs.Parse(rawArgs[1:]); perr != nil {
		return true, perr
	}
	if *socket == "" {
		return true, i18n.Errorf("terva mcp-talkoot-bridge: --socket is required")
	}
	return true, mcpbridge.ServeTeam(context.Background(), os.Stdin, os.Stdout, *socket, bridgeTeamTools())
}

// bridgeTeamTools is the tool list the Talkoot bridge serves, in the order
// the native tools list them, and then ask_user_question.
func bridgeTeamTools() []mcpbridge.TeamTool {
	var out []mcpbridge.TeamTool
	for _, d := range tools.TalkootToolDefs() {
		if slices.Contains(talkoot.BridgeTools, d.Name) {
			out = append(out, mcpbridge.TeamTool{Name: d.Name, Description: d.Description, Schema: d.Schema})
		}
	}
	ask := &tools.AskUserTool{}
	return append(out, mcpbridge.TeamTool{Name: ask.Name(), Description: ask.Description(), Schema: ask.Schema()})
}
