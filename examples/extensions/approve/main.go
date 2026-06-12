// approve — demonstrates the spontaneous open_panel pattern for
// human-in-the-loop tool approval gates.
//
// Registers one LLM-callable tool:
//
//	approve_action(action: string, reason: string)
//
// When the model calls it, a panel opens asking the user to approve
// or deny. The tool goroutine blocks until the user responds; the
// model only sees the result after the user has acted.
//
// Build:
//
//	cd examples/extensions/approve
//	go build -o approve .
//
// Install:
//
//	terva ext install .
//
// Try it — ask terva something like:
//
//	"Request approval to delete the temp directory."
//
// The model will call approve_action; a panel appears in the TUI. Press
// y to approve or n (or esc) to deny. The model receives "approved" or
// "denied: user rejected the action" as the tool result and responds
// accordingly.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/ext"
)

const schema = `{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "description": "Short description of what is about to happen, shown to the user."
    },
    "reason": {
      "type": "string",
      "description": "Why the action is needed, shown to the user."
    }
  },
  "required": ["action"]
}`

func main() {
	e := ext.New("approve", "1.0.0")

	// counter is used to generate unique panel IDs so that concurrent
	// tool calls (unlikely in practice but possible) don't collide.
	var mu sync.Mutex
	var counter int
	nextPID := func() string {
		mu.Lock()
		defer mu.Unlock()
		counter++
		return fmt.Sprintf("approve-%d", counter)
	}

	e.Tool("approve_action", "Ask the user to approve or deny an action before it proceeds.", json.RawMessage(schema),
		func(args json.RawMessage) ext.ToolResult {
			var in struct {
				Action string `json:"action"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return ext.TextErrorResult("invalid args: " + err.Error())
			}
			if strings.TrimSpace(in.Action) == "" {
				return ext.TextErrorResult("action is required")
			}

			pid := nextPID()
			decision := make(chan bool, 1)

			buildLines := func(hint string) []string {
				lines := []string{
					"  Action:  " + in.Action,
				}
				if strings.TrimSpace(in.Reason) != "" {
					lines = append(lines, "  Reason:  "+in.Reason)
				}
				lines = append(lines, "")
				lines = append(lines, "  "+hint)
				return lines
			}

			const footer = "● this panel has focus — y approve  n deny  esc cancel"
			const prompt = "› approve this action? [y/n]"
			const badKey = "› unrecognised key — press y to approve or n to deny"

			// Register key handler before opening the panel so there
			// is no window where a key could arrive unhandled.
			e.OnPanelKey(pid, func(key, text string) {
				switch {
				case key == "rune" && strings.ToLower(text) == "y":
					e.ClosePanel(pid)
					decision <- true
				case key == "rune" && strings.ToLower(text) == "n",
					key == "esc":
					e.ClosePanel(pid)
					decision <- false
				default:
					// Unknown key: re-render with a nudge so the user
					// knows the panel is alive and what to press.
					e.RenderPanel(pid, "Approval required",
						buildLines(badKey), footer)
				}
			}, func() {
				// Host closed the panel (e.g. user navigated away).
				select {
				case decision <- false:
				default:
				}
			})

			// Open the panel spontaneously from inside the tool handler.
			e.OpenPanel(pid, "Approval required", buildLines(prompt), footer)

			// Block until the user responds.
			if <-decision {
				return ext.TextResult("approved")
			}
			return ext.TextErrorResult("denied: user rejected the action")
		})

	if err := e.Run(); err != nil {
		e.Logf("fatal: %v", err)
		os.Exit(1)
	}
}
