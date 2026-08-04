package tools

import (
	"context"
	"encoding/json"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/relaunch"
)

// RestartTool re-execs the running terva into the currently-installed binary
// (Tier-1 self-restart). It is registered only when self-restart is enabled
// (--allow-restart; web mode refuses it on an insecure no-auth listener). It is
// deliberately left out of the permission classifier maps, so in every GATING
// approval mode (ask / auto-edit / workspace — the web default) it prompts the
// operator before running: the "agent restarts itself, with approval" path.
//
// CAVEAT: --approval yolo bypasses the gate for ALL tools, so it skips this
// prompt too — a deliberate v1 choice (yolo means "run freely"; forcing
// approval-even-in-yolo for this one tool was declined). Enabling self-restart
// AND running yolo means the agent can re-exec the daemon without a prompt; use
// yolo with self-restart only when that is acceptable.
type RestartTool struct{}

func (t *RestartTool) Name() string { return "terva_restart" }

func (t *RestartTool) Description() string {
	return i18n.D("tool.terva_restart.description", "Start the terva process again, and use the binary from the current installation. Use this tool to load a new build. An example is a build after you change the code of terva.\n\nThe tool asks the operator for approval in each approval mode that has a gate. The tool interrupts the current session. Connected clients connect again automatically, and the tool reads the transcript from the disk. Use this tool only when the user asks you to restart terva or to apply a new build.")
}

func (t *RestartTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"A short note that tells why the restart is necessary. The tool writes this note in the server log."}},"additionalProperties":false}`)
}

func (t *RestartTool) Execute(ctx context.Context, args json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var in struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(args, &in)
	reason := in.Reason
	if reason == "" {
		reason = "agent request"
	}
	if err := relaunch.Trigger("tool: " + reason); err != nil {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: "restart failed: " + err.Error()}},
			IsError: true,
		}, nil
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "Restarting terva now; the session will reconnect shortly on the new build."}},
	}, nil
}
