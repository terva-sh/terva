package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"terva.sh/terva/packages/agent/restartmarker"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// ArmRestartTool declares that an imminent, intentional SUPERVISOR restart of
// the terva daemon is planned for this session, just before the agent issues the
// restart command itself (e.g. `systemctl --user restart` to apply a changed
// unit — which self-restart via terva_restart cannot do, since the supervisor,
// not this process, must replace it).
//
// Arming writes a short-lived on-disk marker (session id, outgoing build, a
// nonce, an expiry). A SIGTERM inside the window is then treated as planned: the
// replacement process reconciles the interrupted command as expected rather than
// a failure and resumes this exact session. Outside the window, or unarmed, a
// SIGTERM is a stop. terva stays supervisor-agnostic — the agent runs whatever
// restart command; this tool only records the intent.
//
// Like terva_restart, it is registered only when self-restart is enabled and is
// left out of the permission classifier maps, so in every gating approval mode
// it prompts the operator before recording a planned restart.
type ArmRestartTool struct {
	// Session is the wire id of the session this tool instance belongs to — the
	// one that should resume after the restart.
	Session string
	// Home is the data home the marker is written under ($TERVA_HOME); the
	// replacement process reads it from the same place.
	Home string
}

const (
	armDefaultWindow = 15 * time.Second
	armMaxWindow     = 120 * time.Second
	armMinWindow     = 1 * time.Second
)

func (t *ArmRestartTool) Name() string { return "terva_arm_restart" }

func (t *ArmRestartTool) Description() string {
	return i18n.D("tool.terva_arm_restart.description", "Declare that a supervisor restart of the terva daemon will occur for this session. Call this tool immediately before you run the supervisor command yourself. An example of such a command is `systemctl --user restart`, which applies a changed unit. The terva_restart tool starts the same binary again and cannot apply a change to a unit.\n\nThe tool asks the operator for approval. In the armed period, the tool treats the SIGTERM signal that stops the process as planned. The interrupted command is then expected and is not a failure. Clients see a notice about the restart and do not see a failure, and this session continues.\n\nFirst arm the restart, then run the restart command in the armed period. For a restart of the binary only, use terva_restart instead.")
}

func (t *ArmRestartTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"A short note that tells why you plan the restart. The tool shows this note to the operator and in the notice after the restart."},"window_seconds":{"type":"integer","minimum":1,"maximum":120,"description":"The number of seconds that the armed period stays open. The restart must start in this period. The default is 15 seconds, and the maximum is 120 seconds."}},"additionalProperties":false}`)
}

func (t *ArmRestartTool) Execute(_ context.Context, args json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var in struct {
		Reason        string `json:"reason"`
		WindowSeconds int    `json:"window_seconds"`
	}
	_ = json.Unmarshal(args, &in)

	window := armDefaultWindow
	if in.WindowSeconds > 0 {
		window = time.Duration(in.WindowSeconds) * time.Second
	}
	if window < armMinWindow {
		window = armMinWindow
	}
	if window > armMaxWindow {
		window = armMaxWindow
	}

	expires := time.Now().Add(window)
	m := restartmarker.Marker{
		Session:     t.Session,
		FromVersion: buildinfo.Get().Version,
		Reason:      in.Reason,
		Nonce:       restartmarker.NewNonce(),
		ExpiresUnix: expires.Unix(),
	}
	if err := restartmarker.Arm(t.Home, m); err != nil {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: "failed to arm the planned restart: " + err.Error()}},
			IsError: true,
		}, nil
	}
	secs := int(window / time.Second)
	msg := fmt.Sprintf("Planned restart armed for the next %ds. Run the supervisor restart command now (within the window); the interrupted command will be reconciled as a planned restart and this session will resume on the new build. If the window lapses without a restart, the marker expires harmlessly.", secs)
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: msg}},
	}, nil
}
