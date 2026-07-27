package build

import (
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
)

// lazy_tools hiding may engage only where activate_tools exists as the
// reveal path. Before this rule, EnableLazyTools ran unconditionally on the
// flag while activate_tools registered only for base-workspace sessions —
// so flipping the flag on would have hidden extension/world tools in
// chat/play/--no-tools sessions with no way for the model to reveal them.
func TestLazyVisibilityEngages(t *testing.T) {
	withReveal := Resolved{LazyTools: true, ToolRegistry: core.Registry{"activate_tools": &tools.ActivateToolsTool{}}}
	if !lazyVisibilityEngages(&withReveal) {
		t.Error("flag on + activate_tools registered must engage lazy visibility")
	}
	noReveal := Resolved{LazyTools: true, ToolRegistry: core.Registry{}}
	if lazyVisibilityEngages(&noReveal) {
		t.Error("flag on WITHOUT activate_tools must not engage — there is no reveal path")
	}
	flagOff := Resolved{ToolRegistry: core.Registry{"activate_tools": &tools.ActivateToolsTool{}}}
	if lazyVisibilityEngages(&flagOff) {
		t.Error("flag off must never engage lazy visibility")
	}
}
