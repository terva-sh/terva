package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"terva.sh/terva/packages/provider"
)

// scriptedClient is a provider.Client that needs no network and no key. It
// reads the user's prompt the way a very literal model would: numbers in it
// become an add call, and "remember X" becomes a remember call for X. Once
// the request carries the tools' results, it answers with them, marking the
// ones that came back as errors. That is enough for a turn to run every part
// of the loop: the request, the gate, the tools, and the answer.
//
// A real host passes a client from the wire instead (see main.go). The engine
// cannot tell the two apart, which is the point of the Client interface.
type scriptedClient struct{}

func (scriptedClient) Name() string { return "scripted" }

func (scriptedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "scripted", Model: req.Model}
		msg, stop := reply(req)
		for _, c := range msg.Content {
			if t, ok := c.(provider.TextBlock); ok {
				out <- provider.EventTextDelta{Delta: t.Text}
			}
		}
		out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 50, OutputTokens: 10}}
		select {
		case out <- provider.EventDone{Stop: stop, Message: msg}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

var (
	numberPattern   = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
	rememberPattern = regexp.MustCompile(`(?i)\bremember\s+(?:that\s+|to\s+)?(.+)`)
)

func reply(req provider.Request) (provider.Message, provider.StopReason) {
	if len(req.Messages) == 0 {
		return text("There is nothing to answer."), provider.StopEnd
	}
	last := req.Messages[len(req.Messages)-1]

	var results []string
	for _, c := range last.Content {
		if r, ok := c.(provider.ToolResultBlock); ok {
			s := resultText(r)
			if r.IsError {
				s = "error: " + s
			}
			results = append(results, s)
		}
	}
	if len(results) > 0 {
		return text("The tools said: " + strings.Join(results, "; ") + "."), provider.StopEnd
	}

	// Numbers count only before "remember", so a note's digits are not summed.
	prompt := messageText(last)
	arithmetic, remember := prompt, rememberPattern.FindStringSubmatchIndex(prompt)
	if remember != nil {
		arithmetic = prompt[:remember[0]]
	}
	var calls []provider.Content
	if nums := numberPattern.FindAllString(arithmetic, -1); len(nums) > 0 {
		args, _ := json.Marshal(map[string]any{"numbers": json.RawMessage("[" + strings.Join(nums, ",") + "]")})
		calls = append(calls, provider.ToolCallBlock{ID: "call-add", Name: "add", Arguments: args})
	}
	if remember != nil {
		args, _ := json.Marshal(rememberArgs{Note: strings.TrimSpace(prompt[remember[2]:remember[3]])})
		calls = append(calls, provider.ToolCallBlock{ID: "call-remember", Name: "remember", Arguments: args})
	}
	if len(calls) == 0 {
		return text("I have no tool for that: give me numbers to add, or something to remember."), provider.StopEnd
	}
	return provider.Message{Role: provider.RoleAssistant, Content: calls}, provider.StopToolUse
}

func text(s string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: s}}}
}

func messageText(m provider.Message) string {
	var parts []string
	for _, c := range m.Content {
		if t, ok := c.(provider.TextBlock); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}

func resultText(r provider.ToolResultBlock) string {
	var parts []string
	for _, c := range r.Content {
		if t, ok := c.(provider.TextBlock); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}
