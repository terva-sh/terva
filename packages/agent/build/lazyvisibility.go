package build

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/lazytools"
)

// lazyVisibilityEngages reports whether this build may hide tool groups:
// the lazy_tools flag is on AND activate_tools exists as the reveal path.
// The two are decided in different places — the registry at Resolve (which
// registers activate_tools only for base-workspace sessions), the agent at
// NewAgent — and their agreement is the flip-safety contract: hiding groups
// in a session that never registered activate_tools (chat, play, --no-tools,
// --no-workspace-tools) would bury extension and world tools with no way for
// the model to bring them back. Play acts ONLY through world-extension
// tools, so that is a hard break, not a degradation.
// TestLazyVisibilityEngages pins the rule.
func lazyVisibilityEngages(r *Resolved) bool {
	return r.LazyTools && r.ToolRegistry["activate_tools"] != nil
}

// LazyTools returns the options that connect lazy tool visibility to an
// agent, advertising the core group plus the given always-active groups. When
// the engine connects them, the visibility's inactive-group note goes into
// asm's frame, so pass the Assembler the agent is built over, or nil for an
// agent built over another assembler. Resolved.NewAgent passes them where
// lazyVisibilityEngages. A test that builds its own agent passes them to have
// lazy mode.
//
// Each call makes one Visibility, which serves one agent, so call it once per
// agent.
func LazyTools(asm *Assembler, active ...string) []core.Option {
	lz := lazytools.New(active...)
	opts := []core.Option{core.WithComponent(lz)}
	if asm != nil {
		opts = append(opts, core.WithComponent(lazyNote{asm: asm, v: lz}))
	}
	return opts
}

// lazyNote puts a Visibility's note in an Assembler's frame when the engine
// binds it, so an agent that core.New refuses leaves the assembler untouched.
type lazyNote struct {
	asm *Assembler
	v   *lazytools.Visibility
}

var _ core.Binder = lazyNote{}

// Bind implements core.Binder.
func (n lazyNote) Bind(*core.Agent) { n.asm.setLazy(n.v) }
