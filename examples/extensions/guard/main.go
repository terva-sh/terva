// guard — phase 3 example: event subscriptions + tool-call interception.
//
// What it does:
//
//   - Subscribes to turn_start, tool_call, and turn_end events and
//     writes a one-line audit entry per event to its own log file.
//
//   - Intercepts every tool call. If the tool is `bash` and the
//     command matches a danger pattern (rm -rf, sudo, dd of=/, etc.)
//     the call is refused and the model gets a one-line refusal it
//     can react to. Everything else passes through.
//
// Build it:
//
//	cd examples/extensions/guard
//	go build -o guard .
//
// Drop it next to its extension.json under $TERVA_HOME/extensions/guard/
// (or `terva ext install ./guard` from this directory) and the next terva
// session will load it automatically.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ext"
)

var dangerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-rf\b`),
	regexp.MustCompile(`(?i)\bsudo\b`),
	regexp.MustCompile(`(?i)\bdd\s+of=/`),
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)\b:\s*\(\s*\)\s*\{\s*:\|:`), // fork bomb
	regexp.MustCompile(`(?i)\bchmod\s+-R\s+777\b`),
}

func main() {
	e := ext.New("guard", "1.0.0")

	auditPath := filepath.Join(os.TempDir(), "terva-guard-audit.log")
	auditFile, _ := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if auditFile != nil {
		defer auditFile.Close()
	}
	audit := func(format string, args ...any) {
		line := fmt.Sprintf("%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
		if auditFile != nil {
			auditFile.WriteString(line)
		}
		e.Logf("%s", strings.TrimRight(line, "\n"))
	}

	// Lifecycle observers.
	e.On("session_start", func(ev ext.Event) { audit("[guard] session_start") })
	e.On("turn_start", func(ev ext.Event) { audit("[guard] turn_start step=%d", ev.Step) })
	e.On("tool_call", func(ev ext.Event) {
		audit("[guard] tool_call %s args=%s", ev.ToolName, string(ev.ToolArgs))
	})
	e.On("turn_end", func(ev ext.Event) { audit("[guard] turn_end stop=%s err=%s", ev.Stop, ev.Error) })

	// Tool-call gate.
	e.InterceptToolCall(func(toolName string, args json.RawMessage) (bool, string) {
		if toolName != "bash" {
			return true, "" // only guard bash
		}
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return true, ""
		}
		for _, pat := range dangerPatterns {
			if pat.MatchString(in.Command) {
				audit("[guard] BLOCKED bash: %s (matched %s)", in.Command, pat.String())
				return false, fmt.Sprintf("guard refused: command matches the danger pattern %q. Ask the user before running this.", pat.String())
			}
		}
		return true, ""
	})

	if err := e.Run(); err != nil {
		e.Logf("fatal: %v", err)
	}
}
