package build

import (
	"context"
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// hostToolSource is the slice of the extension manager the host-tool
// dispatcher needs: whether a tool name is extension-owned, so a
// host_tool_call can be refused from re-entering an extension.
type hostToolSource interface {
	HasTool(name string) bool
}

// buildHostToolDispatcher returns the handler that runs host tools for
// extension host_tool_call frames (protocol 3, extproto.HostToolCallFromExt).
// It runs the named tool through the agent's live registry and the SAME
// approval gate a model-issued call uses, so an extension gains reach,
// not authority. It refuses extension-owned tools, so a host_tool_call
// cannot recurse back into an extension (depth-1, mirroring swarm's
// MAX_DEPTH=1): the supported targets are the host's own built-in and
// MCP tools (read/grep/glob/bash/web/…). Trust is already enforced
// upstream — an untrusted project's extensions never load, so a loaded
// extension is either the user's own or one a trusted project shipped.
func buildHostToolDispatcher(ag *core.Agent, gate *core.ConfirmGate, mgr hostToolSource) extdriver.HostToolDispatcher {
	return func(ctx context.Context, extName, toolName string, args json.RawMessage, silent bool) ([]extproto.ContentBlock, bool) {
		errOut := func(msg string) ([]extproto.ContentBlock, bool) {
			return []extproto.ContentBlock{{Type: "text", Text: msg}}, true
		}
		if ag == nil {
			return errOut("host_tool_call: no agent available")
		}
		if mgr != nil && mgr.HasTool(toolName) {
			return errOut(fmt.Sprintf("host_tool_call: %q is an extension tool; host_tool_call may only run the host's own built-in or MCP tools", toolName))
		}
		tool, ok := ag.LookupTool(toolName)
		if !ok {
			return errOut(fmt.Sprintf("host_tool_call: no such host tool %q", toolName))
		}
		// Same permission gate as a model-issued call: an extension never
		// bypasses approval. (silent is the extension's UI hint, not a
		// permission override.)
		preview := core.BuildPreview(args, 120)
		if allowed, reason, _ := gate.Check(toolName, args, preview); !allowed {
			return errOut(fmt.Sprintf("host_tool_call: %q denied: %s", toolName, reason))
		}
		res, err := tool.Execute(ctx, args, nil)
		if err != nil {
			return errOut(fmt.Sprintf("host_tool_call: %q failed: %v", toolName, err))
		}
		return contentBlocksFromResult(res.Content), res.IsError
	}
}

// contentBlocksFromResult converts a tool result's content into the wire
// content blocks an extension receives. Text passes through; non-text
// content (images, …) isn't representable to a script expecting text, so
// it is described rather than silently dropped.
func contentBlocksFromResult(content []provider.Content) []extproto.ContentBlock {
	out := make([]extproto.ContentBlock, 0, len(content))
	for _, c := range content {
		switch b := c.(type) {
		case provider.TextBlock:
			out = append(out, extproto.ContentBlock{Type: "text", Text: b.Text})
		default:
			out = append(out, extproto.ContentBlock{Type: "text", Text: fmt.Sprintf("[%T content omitted]", c)})
		}
	}
	return out
}

// WireHostToolDispatcher lets an extension run the host's own tools via
// protocol-3 host_tool_call, under the same registry and approval gate
// the model uses. No-op when extensions or the agent are absent. Called
// from every agent-construction seam that already wires the extension
// tool-call ladder, so all run modes get it.
func WireHostToolDispatcher(ag *core.Agent, extMgr *extensions.Manager, gate *core.ConfirmGate) {
	if ag == nil || extMgr == nil {
		return
	}
	extMgr.SetHostToolDispatcher(buildHostToolDispatcher(ag, gate, extMgr))
}
