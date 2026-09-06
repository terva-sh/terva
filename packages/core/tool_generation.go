package core

import "context"

// toolGeneration keeps dispatch and classification together. The context
// carries its owner so a child agent cannot inherit its parent's tool set.
type toolGeneration struct {
	agent    *Agent
	tools    Registry
	readOnly *ReadOnlySet
}

type toolGenerationKey struct{}

// SetToolsWithReadOnly publishes tools and their classification together.
// Build a fresh registry before calling this method; never mutate it after
// publication. The classification is copied. Nil classifies every tool as
// mutating. The return value describes model-visible changes, like SetTools.
func (a *Agent) SetToolsWithReadOnly(reg Registry, readOnly *ReadOnlySet) bool {
	ro := readOnly.Snapshot()
	if ro == nil {
		ro = NewReadOnlySet()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := !registryEqual(a.Tools, reg)
	a.Tools, a.ReadOnly = reg, ro
	return changed
}

// ToolsWithReadOnlySnapshot returns a coherent registry and classification.
// The caller must treat the registry as immutable; the set is an independent copy.
func (a *Agent) ToolsWithReadOnlySnapshot() (Registry, *ReadOnlySet) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Tools, a.ReadOnly.Snapshot()
}

// ToolForCall resolves a tool with the classification its gate must use.
// Nested calls keep the calling turn's generation. Calls outside a turn pin
// the current generation. Pass the returned context to both Check and Execute.
func (a *Agent) ToolForCall(ctx context.Context, name string) (context.Context, Tool, bool) {
	g, _ := ctx.Value(toolGenerationKey{}).(toolGeneration)
	if g.agent != a {
		reg, ro := a.ToolsWithReadOnlySnapshot()
		g = toolGeneration{agent: a, tools: reg, readOnly: ro}
		ctx = context.WithValue(ctx, toolGenerationKey{}, g)
	}
	t, ok := g.tools[name]
	return ctx, t, ok
}
