// Command bridge is a thin test entrypoint for mcpbridge.Serve — the same logic
// the real `terva mcp-approval-bridge` subcommand runs, built as a standalone
// binary so the package test can spawn it through terva's own MCP client and
// prove the stdio server end to end. It is NOT shipped. With -team it runs
// mcpbridge.ServeTeam over two stand-in tools instead.
//
// Run as `bridge mcp-talkoot-bridge --socket P`, the shape a --team-socket run
// gives terva, it serves every tool in talkoot.BridgeTools, so a test can put
// it in terva's place.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/talkoot"
)

func main() {
	args := os.Args[1:]
	talkootCmd := len(args) > 0 && args[0] == "mcp-talkoot-bridge"
	if talkootCmd {
		args = args[1:]
	}
	fs := flag.NewFlagSet("bridge", flag.ExitOnError)
	socket := fs.String("socket", "", "unix socket of the orchestrator's approval endpoint")
	team := fs.Bool("team", false, "serve the Talkoot team tools instead of the approval tool")
	_ = fs.Parse(args)
	if *socket == "" {
		fmt.Fprintln(os.Stderr, "bridge: --socket is required")
		os.Exit(2)
	}
	serve := func() error { return mcpbridge.Serve(context.Background(), os.Stdin, os.Stdout, *socket) }
	switch {
	case talkootCmd:
		var tools []mcpbridge.TeamTool
		for _, name := range talkoot.BridgeTools {
			tools = append(tools, mcpbridge.TeamTool{Name: name, Description: "Stand-in for " + name + ".", Schema: json.RawMessage(`{"type":"object","properties":{}}`)})
		}
		serve = func() error { return mcpbridge.ServeTeam(context.Background(), os.Stdin, os.Stdout, *socket, tools) }
	case *team:
		tools := []mcpbridge.TeamTool{
			{Name: "talkoot_send", Description: "Send an envelope.", Schema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string"}}}`)},
			{Name: "talkoot_roster", Description: "List the members.", Schema: json.RawMessage(`{"type":"object","properties":{}}`)},
		}
		serve = func() error { return mcpbridge.ServeTeam(context.Background(), os.Stdin, os.Stdout, *socket, tools) }
	}
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, "bridge:", err)
		os.Exit(1)
	}
}
