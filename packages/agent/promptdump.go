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
	out, err := promptDumpText(args)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, out)
	return nil
}

// promptDumpText renders the dump for the requested mode. Split from the
// printing so a test can assert on the assembled output.
func promptDumpText(args build.Args) (string, error) {
	r, err := build.Resolve(args, false)
	if err != nil {
		return "", err
	}
	// The live flow injects durable memory into the cached prefix at session
	// bind (RefreshMemory); a dump binds no session, so mirror that here or
	// the dump under-reports every home that has memory by the whole block.
	build.RefreshMemory(&r)
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
		return r.BuildPromptSizes(msgs).Text(), nil
	}
	m := r.BuildPromptManifest(msgs)
	switch args.DumpPrompt {
	case "json":
		return m.JSON(), nil
	case "raw":
		return m.Raw(), nil
	default:
		return m.Text(), nil
	}
}
