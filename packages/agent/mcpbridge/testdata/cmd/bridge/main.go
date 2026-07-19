// Command bridge is a thin test entrypoint for mcpbridge.Serve — the same logic
// the real `terva mcp-approval-bridge` subcommand runs, built as a standalone
// binary so the package test can spawn it through terva's own MCP client and
// prove the stdio server end to end. It is NOT shipped.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/mcpbridge"
)

func main() {
	socket := flag.String("socket", "", "unix socket of the orchestrator's approval endpoint")
	flag.Parse()
	if *socket == "" {
		fmt.Fprintln(os.Stderr, "bridge: --socket is required")
		os.Exit(2)
	}
	if err := mcpbridge.Serve(context.Background(), os.Stdin, os.Stdout, *socket); err != nil {
		fmt.Fprintln(os.Stderr, "bridge:", err)
		os.Exit(1)
	}
}
