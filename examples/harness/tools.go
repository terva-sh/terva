package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// addTool sums numbers. It changes nothing, so it needs no gate of its own.
type addTool struct{}

func (addTool) Name() string        { return "add" }
func (addTool) Description() string { return "Add a list of numbers and return the sum." }
func (addTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"numbers":{"type":"array","items":{"type":"number"}}},"required":["numbers"]}`)
}

func (addTool) Execute(_ context.Context, args json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var in struct {
		Numbers []float64 `json:"numbers"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return textResult("add: "+err.Error(), true), nil
	}
	var sum float64
	for _, n := range in.Numbers {
		sum += n
	}
	return textResult(strconv.FormatFloat(sum, 'g', -1, 64), false), nil
}

// notebook is the host's state the remember tool writes to. It lives in
// memory, like everything else here.
type notebook struct {
	mu    sync.Mutex
	notes []string
}

func (n *notebook) add(s string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notes = append(n.notes, s)
	return len(n.notes)
}

func (n *notebook) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notes...)
}

type rememberArgs struct {
	Note string `json:"note"`
}

// rememberTool keeps a note in the notebook. The gate decides which notes it
// may keep; the tool itself trusts the call it is given.
type rememberTool struct{ notes *notebook }

func (rememberTool) Name() string        { return "remember" }
func (rememberTool) Description() string { return "Keep a short note for later." }
func (rememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"}},"required":["note"]}`)
}

func (t rememberTool) Execute(_ context.Context, args json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var in rememberArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return textResult("remember: "+err.Error(), true), nil
	}
	return textResult(fmt.Sprintf("kept note %d", t.notes.add(in.Note)), false), nil
}

func textResult(s string, isError bool) core.ToolResult {
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: s}}, IsError: isError}
}
