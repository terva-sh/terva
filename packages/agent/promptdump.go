package agent

import (
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/provider"
)

// runPromptDump implements --dump-prompt: assemble the prompt that would be
// sent for the pending turn, render the source-of-truth manifest, print it to
// stdout, and exit before any model call. Resolve runs with requireCred=false,
// so it needs no API key or tokens — a debugging + offline-assertion tool.
// Output goes to stdout so `--dump-prompt=json | jq ...` works.
func runPromptDump(args build.Args) error {
	r, err := build.Resolve(args, false)
	if err != nil {
		return err
	}
	var msgs []provider.Message
	// A card greeting shows as messages[0], mirroring a fresh session.
	if g := strings.TrimSpace(r.CardGreeting); g != "" {
		msgs = append(msgs, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: g}},
			Meta:    map[string]string{"source": "card:greeting"},
		})
	}
	// The pending user turn (from -p / positional) so the tail keyword scan and
	// the messages section reflect the turn being assembled.
	if p := strings.TrimSpace(args.Prompt); p != "" {
		msgs = append(msgs, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: p}},
		})
	}
	if args.DumpPrompt == "sizes" {
		fmt.Fprintln(os.Stdout, r.BuildPromptSizes(msgs).Text())
		return nil
	}
	m := r.BuildPromptManifest(msgs)
	switch args.DumpPrompt {
	case "json":
		fmt.Fprintln(os.Stdout, m.JSON())
	case "raw":
		fmt.Fprintln(os.Stdout, m.Raw())
	default:
		fmt.Fprintln(os.Stdout, m.Text())
	}
	return nil
}
