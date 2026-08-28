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
	// Shared are files this call published for the human reading the transcript
	// (see [SharedFile]). Like Details it is not sent to the LLM — the model's
	// copy is a line of Content saying the share happened — but unlike Details it
	// is first-class, because Details stops at the event wire and a card only the
	// in-process UI can see is no use to the remote panel this exists for.
	//
	// Each entry's CallID is filled in by the agent loop, not by the tool.
	Shared []SharedFile
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

// ToolEssential reports whether a tool is load-bearing — declared essential by
// its extension (register_tool essential) so it stays advertised every turn
// even when its capability group is inactive under lazy tool visibility. This
// is what lets an extension whose static guidance names a tool ("search the
// index before reading") keep that tool in the advertised set, instead of the
// prompt pointing at a tool sitting deferred behind activate_tools. Read
// through a structural interface, like ToolGroup, so core need not import the
// wrapper package; built-ins and MCP tools never implement it (always false).
// Essential is visibility only — a hidden or shown tool is dispatched and
// permission-gated identically.
func ToolEssential(t Tool) bool {
	if e, ok := t.(interface{ Essential() bool }); ok {
		return e.Essential()
	}
	return false
}

// ToolPreview returns the one-line summary a confirmation prompt shows for a
// call to t. A tool contributes its own by implementing
// Preview(json.RawMessage, int) string, read through a structural interface
// like ToolGroup and ToolEssential so core need not import the implementing
// packages. A nil tool, a tool that does not implement it, or one that
// returns "" all fall back to BuildPreview — so a prompt changes only for a
// tool that opts in, and a contributor may decline per call by returning "".
//
// This exists because BuildPreview can only read the args generically: it
// picks the first recognisable field, which for a tool whose whole argument
// is a program says nothing useful. The tool itself can say what the call
// will do ("read x5, write x2") where a generic reader cannot.
//
// Display only. It decides nothing: the gate checks the same args this is
// built from, and the result is truncated here rather than trusted, so a
// contributor cannot flood the prompt. A tool that misreports its own
// preview still passes through every rung of the ladder unchanged.
func ToolPreview(t Tool, args json.RawMessage, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 120
	}
	if t != nil {
		if p, ok := t.(interface {
			Preview(json.RawMessage, int) string
		}); ok {
			if s := p.Preview(args, maxLen); s != "" {
				return truncatePreview(s, maxLen)
			}
		}
	}
	return BuildPreview(args, maxLen)
}

// Get looks up a tool by name.
func (r Registry) Get(name string) (Tool, error) {
	t, ok := r[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	return t, nil
}
