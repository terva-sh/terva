package agent

import (
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// promptDumpWire renders the request as the provider would serialize it, in the
// diffable JSONL shape DumpRequestJSONL documents.
//
// Two fields are deliberately absent, and both are absent for the same reason —
// they cannot change the answer this mode exists to give:
//
//   - EphemeralContext (the per-turn tail) is appended AFTER the prompt-cache
//     breakpoint, so it is not part of the prefix a cache matches on. It also
//     needs a live agent and an extension manager to compose, neither of which
//     a credential-free dump has.
//   - PromptCacheKey is a routing hint rather than a cache partition (measured;
//     a key that had never been sent reads back another key's payload), so it
//     is not prefix bytes either. It is still filled in from --session when
//     there is one, because a dump that can show it should.
func promptDumpWire(args build.Args, r build.Resolved, msgs []provider.Message) (string, error) {
	req := provider.Request{
		Model:            r.Model,
		System:           r.SystemPrompt,
		Messages:         msgs,
		Tools:            r.ToolRegistry.Specs(),
		Reasoning:        r.Reasoning,
		ReasoningSet:     r.ReasoningSet,
		ReasoningSummary: r.ReasoningSummary,
		Temperature:      r.Temperature,
	}
	if p := strings.TrimSpace(args.Session); p != "" {
		if meta, err := core.ReadSessionMeta(p); err == nil {
			req.PromptCacheKey = meta.ID
		}
	}
	// r.AuthMethod, not a credential: on Anthropic the subscription body differs
	// from the api-key one, and the dump has to show the one this run would send.
	b, err := provider.DumpRequestJSONL(r.Provider, r.AuthMethod, req)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

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
	// A --session on the command line means "the prompt for the pending turn of
	// THIS conversation". Without replaying it the dump answers a question
	// nobody asked — the prompt for turn 1 of a session that is on turn 80 —
	// which is exactly the gap that made it useless for the prompt-cache work.
	// ReadSessionMessages is the read-only loader, so pointing a dump at a
	// session that is live (or that the user cannot write) neither takes an
	// append handle on it nor needs one.
	if p := strings.TrimSpace(args.Session); p != "" {
		prior, err := core.ReadSessionMessages(p)
		if err != nil {
			return "", err
		}
		msgs = append(msgs, prior...)
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
	if args.DumpPrompt == "wire" {
		return promptDumpWire(args, r, msgs)
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
