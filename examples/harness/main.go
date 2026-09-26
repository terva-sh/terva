// Command harness is a working agent built from terva's engine and wire alone,
// with none of terva's harness: no config files, no session store, no tool set
// but its own. It is where to start when terva's conventions are not the ones
// a host wants. docs/embedding.md compares it with the other two ways to embed
// terva.
//
//	go run ./examples/harness "what is 2 + 3? remember to buy milk"
//
// With no ANTHROPIC_API_KEY it talks to a scripted fake client, so it runs
// anywhere. With one, it talks to Anthropic; HARNESS_MODEL picks the model.
//
// This file is the host's edge, and the only place the example reads its
// environment. Everything past it takes values.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func main() {
	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 2 + 3? Also remember my password is hunter2"
	}
	catalog := hostCatalog()
	client, model := clientFromEnv(catalog)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	notes := &notebook{}
	store := core.NewMemoryTranscriptStore()
	agent, err := newAgent(client, model, catalog, notes, store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
	if err := agent.Run(ctx, core.PromptInput{Text: prompt}, printEvent); err != nil {
		fmt.Fprintln(os.Stderr, "harness:", err)
		os.Exit(1)
	}
	fmt.Printf("\nnotes kept: %d, messages in the store: %d\n", len(notes.all()), len(store.Transcript().Messages))
}

// clientFromEnv is where a credential enters. The wire takes it as a value;
// it never looks one up. The client reads the same catalog as the agent.
func clientFromEnv(catalog provider.ModelCatalog) (provider.Client, string) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return scriptedClient{}, "scripted"
	}
	model := os.Getenv("HARNESS_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}
	return provider.WithCatalog(provider.NewAnthropic(key, ""), catalog), model
}

func printEvent(ev core.AgentEvent) {
	switch e := ev.(type) {
	case core.EvTextDelta:
		fmt.Print(e.Delta)
	case core.EvToolCall:
		// The name only. The arguments are what the model sent, before the
		// gate has ruled on them, so printing them would show a refused note.
		fmt.Printf("[tool %s]\n", e.Name)
	case core.EvToolResult:
		state := "ok"
		if e.Result.IsError {
			state = "error"
		}
		fmt.Printf("[result %s: %s]\n", state, resultText(provider.ToolResultBlock{Content: e.Result.Content}))
	case core.EvTurnEnd:
		if e.Err != nil {
			fmt.Fprintln(os.Stderr, "turn failed:", e.Err)
		}
	}
}
