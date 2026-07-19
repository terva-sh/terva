package agent

import (
	"context"
	"flag"
	"os"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/i18n"
)

// runMCPApprovalBridgeCommand handles `terva mcp-approval-bridge --socket <path>`
// — the stdio MCP approval server a worker runs as its permission tool. It reads
// MCP jsonrpc on stdin/stdout (its parent CLI drives it) and relays each approval
// question to the orchestrating terva over the unix socket at --socket, whose
// 0600 permissions are the auth. See packages/agent/mcpbridge.
//
// It is a companion binary path, not a mode: no config, no credentials, no home.
// A Claude Code worker points --permission-prompt-tool at it via --mcp-config; a
// `terva rpc --portable` worker points --approval-tool at it through terva's own
// MCP client. Returns handled=false when rawArgs isn't this command, mirroring
// the other subcommand routers.
func runMCPApprovalBridgeCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "mcp-approval-bridge" {
		return false, nil
	}
	fs := flag.NewFlagSet("mcp-approval-bridge", flag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket path of the orchestrating terva's approval endpoint")
	if perr := fs.Parse(rawArgs[1:]); perr != nil {
		return true, perr
	}
	if *socket == "" {
		return true, i18n.Errorf("terva mcp-approval-bridge: --socket is required")
	}
	return true, mcpbridge.Serve(context.Background(), os.Stdin, os.Stdout, *socket)
}
