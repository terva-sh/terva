package tools

import (
	"context"
	"encoding/json"

	"terva.sh/terva/packages/core"
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
	return "Restart the terva process into the currently-installed binary to pick up a fresh build (e.g. after terva's own code was changed and reinstalled). Requires operator approval and interrupts the current session; connected clients reconnect automatically and the transcript is restored from disk. Use only when asked to restart or apply a new build."
}

func (t *RestartTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"short note on why a restart is needed (shown in the server log)"}},"additionalProperties":false}`)
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
