//go:build !terva_scripting

package build

import "terva.sh/terva/packages/core"

// ScriptingSupported reports whether this binary carries the jsengine
// scripting consumer (the code_execution tool). Built without
// terva_scripting: false — the tool is absent from the registry, and a
// surface offering it must say "this binary was built without scripting"
// rather than failing obscurely.
func ScriptingSupported() bool { return false }

// wireScriptingHostCall is the no-op twin of the terva_scripting hook in
// scripting_on.go (called from WireHostToolDispatcher on every
// agent-construction seam).
func wireScriptingHostCall(*core.Agent, *core.ConfirmGate) {}
