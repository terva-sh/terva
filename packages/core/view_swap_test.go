package core

import (
	"context"
	"encoding/json"
	"testing"
)

// viewTool is a minimal Tool whose model-facing surface (name, description,
// schema) is fully parameterized, for exercising registryEqual.
type viewTool struct {
	name, desc string
	schema     string
}

func (t viewTool) Name() string            { return t.name }
func (t viewTool) Description() string     { return t.desc }
func (t viewTool) Schema() json.RawMessage { return json.RawMessage(t.schema) }
func (t viewTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	return ToolResult{}, nil
}

// TestSetSystemReportsChange: SetSystem is the cache-break signal's source of
// truth — it must report a real diff and stay silent on an identical
// re-install (rebuilds re-render the same prompt far more often than not).
func TestSetSystemReportsChange(t *testing.T) {
	a := NewAgent(nil, "m", "original", Registry{})
	if a.SetSystem("original") {
		t.Error("identical system prompt must not report a change")
	}
	if !a.SetSystem("rewritten") {
		t.Error("a different system prompt must report a change")
	}
	if a.System != "rewritten" {
		t.Errorf("System = %q after swap", a.System)
	}
}

// TestSetToolsReportsChange: the verdict must track the model-facing surface
// (names, descriptions, schemas — what the prompt cache serializes), not
// implementation identity: re-installing equivalent tool values is silent,
// while a name, description, or schema diff reports.
func TestSetToolsReportsChange(t *testing.T) {
	base := func() Registry {
		return Registry{
			"read":  viewTool{name: "read", desc: "read a file", schema: `{"type":"object"}`},
			"write": viewTool{name: "write", desc: "write a file", schema: `{"type":"object"}`},
		}
	}
	a := NewAgent(nil, "m", "", base())

	if a.SetTools(base()) {
		t.Error("an equivalent registry (fresh values, same surface) must not report a change")
	}
	// A withheld tool (plan mode) is a surface change.
	planned := base()
	delete(planned, "write")
	if !a.SetTools(planned) {
		t.Error("removing a tool must report a change")
	}
	// Restoring it is one too.
	if !a.SetTools(base()) {
		t.Error("restoring a tool must report a change")
	}
	// Same names, changed schema — the serialized prefix differs.
	reschema := base()
	reschema["write"] = viewTool{name: "write", desc: "write a file", schema: `{"type":"object","required":["path"]}`}
	if !a.SetTools(reschema) {
		t.Error("a schema change must report a change")
	}
	// Same names, changed description.
	redesc := base()
	redesc["read"] = viewTool{name: "read", desc: "read a file (fast)", schema: `{"type":"object"}`}
	if !a.SetTools(redesc) {
		t.Error("a description change must report a change")
	}
}
