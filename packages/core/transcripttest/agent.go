package transcripttest

import (
	"context"
	"encoding/json"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// newAgent builds the agent the suite drives: a scripted client, one tool, and
// no gate to speak of. The suite tests stores, not policy.
func newAgent() *core.Agent {
	a, err := core.New(scriptedClient{}, "scripted", core.WithAssembler(core.StaticSystem("You echo.")),
		core.WithTools(core.Registry{"echo": echoTool{}}), core.WithGate(core.AllowAll))
	if err != nil {
		panic(err)
	}
	return a
}

// scriptedClient calls echo on the first request of a turn and answers in
// text once the request carries the result. Each request reports usage, so
// the store sees usage rows as well as messages.
type scriptedClient struct{}

func (scriptedClient) Name() string { return "scripted" }

func (scriptedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "scripted", Model: req.Model}
		msg, stop := provider.Message{Role: provider.RoleAssistant}, provider.StopToolUse
		answered := false
		if n := len(req.Messages); n > 0 {
			for _, c := range req.Messages[n-1].Content {
				if _, ok := c.(provider.ToolResultBlock); ok {
					answered = true
				}
			}
		}
		if answered {
			msg.Content = []provider.Content{provider.TextBlock{Text: "done"}}
			stop = provider.StopEnd
		} else {
			msg.Content = []provider.Content{provider.ToolCallBlock{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"text":"hello"}`)}}
		}
		out <- provider.EventUsage{Usage: provider.Usage{
			InputTokens: 100 * (len(req.Messages) + 1), OutputTokens: 7, CacheReadTokens: 30, CostUSD: 0.001,
		}}
		select {
		case out <- provider.EventDone{Stop: stop, Message: msg}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "Echo the text back." }
func (echoTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}

func (echoTool) Execute(_ context.Context, args json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var in struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(args, &in)
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: in.Text}}}, nil
}
