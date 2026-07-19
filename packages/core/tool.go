// Package core implements the agent loop, tool runtime, and session
// persistence. It is provider-agnostic: it talks to an LLM only through
// the provider.Client interface.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"terva.sh/terva/packages/provider"
)

// Tool is a capability the agent can invoke.
type Tool interface {
	// Name is the unique tool id shown to the LLM.
	Name() string
	// Description is a one-line summary shown to the LLM.
	Description() string
	// Schema is a JSON Schema object for Execute's args.
	Schema() json.RawMessage
	// Execute runs the tool. progress may be called any number of times
	// with partial textual output (for UIs); it is not sent to the LLM.
	Execute(ctx context.Context, args json.RawMessage, progress func(string)) (ToolResult, error)
}

// ToolResult is the outcome of Tool.Execute.
type ToolResult struct {
	// Content is sent back to the LLM (text and/or images).
	Content []provider.Content
	// IsError marks this result as an error to the LLM.
	IsError bool
	// Details is arbitrary data for UIs and logs; not sent to the LLM.
	Details any
	// LinesAdded/LinesRemoved are the line-change counts of a file-mutating
	// tool (edit ships its diff's +/- tallies, write its written line count).
	// First-class rather than buried in Details so they survive the event
	// wire (status-bar Δ segment on remote clients); zero for everything else.
	LinesAdded   int
	LinesRemoved int
}

// Registry is a name->Tool map.
type Registry map[string]Tool

// Specs returns the tool definitions to advertise to the LLM: every
// registered tool. See SpecsVisible for advertising a subset.
func (r Registry) Specs() []provider.Tool { return r.SpecsVisible(nil) }

// SpecsVisible returns the tool definitions to advertise to the LLM, restricted
// to the tools the visible predicate admits (a nil predicate admits all — the
// Specs behavior). This filters only the *advertised* surface: the registry
// itself is not narrowed, so Get and the agent's dispatch still resolve every
// registered tool, and the permission gate still fires — a tool hidden here
// stays callable and stays gated. Advertisement is not authority (retro H2·b's
// visibility ≠ authority invariant).
//
// The output is sorted by tool name so the order is stable across requests.
// This is load-bearing for provider-side prompt caching: providers prefix-match
// tool definitions, and Go's map iteration order is randomized per call, which
// would otherwise bust the cache every single turn. The predicate must be a
// pure function of the name so the advertised set (hence the cached prefix) is
// stable when nothing has changed.
func (r Registry) SpecsVisible(visible func(name string) bool) []provider.Tool {
	names := make([]string, 0, len(r))
	for name := range r {
		if visible != nil && !visible(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]provider.Tool, 0, len(names))
	for _, name := range names {
		t := r[name]
		out = append(out, provider.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return out
}

// CoreToolGroup is the always-visible capability group of first-party built-in
// tools — the coding tools that ARE the task, never an optional capability
// (retro H2·b). Lazy tool visibility never hides the core group.
const CoreToolGroup = "core"

// ToolGroup classifies a tool into its capability group for lazy tool
// visibility (retro H2·b). An extension or MCP tool reports its source through
// an Extension() accessor (the extension name, or "mcp:<server>"); a built-in
// that is an optional capability rather than a core coding tool may opt into a
// named group through a ToolGroupName() accessor (checked first — it is an
// explicit classification, where Extension() is a provenance fact); everything
// else is a built-in and belongs to CoreToolGroup. Both accessors are
// structural interfaces so core need not import the implementing packages.
func ToolGroup(t Tool) string {
	if g, ok := t.(interface{ ToolGroupName() string }); ok {
		if name := g.ToolGroupName(); name != "" {
			return name
		}
	}
	if e, ok := t.(interface{ Extension() string }); ok {
		if g := e.Extension(); g != "" {
			return g
		}
	}
	return CoreToolGroup
}

// Get looks up a tool by name.
func (r Registry) Get(name string) (Tool, error) {
	t, ok := r[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	return t, nil
}
