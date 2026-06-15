// Command extstub is a deterministic test extension for the ACP
// extension-wiring integration test. It mirrors extensions/testdata's
// echostub: a single compiled Go binary with zero interpreter/stdin
// buffering variance, so a missing reply means a genuine host bug, not
// a flaky stub.
//
// Protocol: emit hello, register two tools + a set of slash commands, then
// ready, then answer each tool_call with a tool_result and each
// command_invoked with a command_response.
//
//   - reader_tool  (read_only:true)  — a side-effect-free tool. It joins
//     the host's read-only classification, so plan/workspace approval
//     modes must run it WITHOUT a permission prompt. It answers "read-ok".
//   - writer_tool  (read_only:false) — a mutating tool. The bundle's
//     extension.json declares `writer_tool -> ask`, so the host gate must
//     issue session/request_permission before it runs. It answers
//     "wrote:<path>" echoing the path arg so the test can confirm it ran.
//
// Slash commands, one per response action, so the ACP command surface test
// can drive each translation:
//
//   - showinfo    -> display  (renders text, no model turn)
//   - dowork      -> prompt   (hands the agent a task; the host runs a real
//     model turn with the returned text)
//   - fillin      -> insert   (TUI-only; the host degrades it to a display)
//   - showpanel   -> open_panel (TUI-only; the host degrades it to a display)
//   - boom        -> error    (a command-level failure)
//   - help        -> display  (NAME COLLISION with the built-in /help; the
//     built-in must win, so this response is never reached)
//
// It also contributes to the model's context so the /context inspector test has
// something to show: a STATIC register_context block (system guidance) and a
// live context_card. These survive a /reload-ext (the stub re-emits them on
// respawn), so the command surface is identical before and after a reload.
//
// The extstub is paired with an extension.json the test writes alongside
// it, whose `permissions` key carries the writer_tool ask rule that the
// real buildPermissionPolicy path compiles into the gate.
package main

import (
	"bufio"
	"encoding/json"
	"os"

	"terva.sh/terva/packages/agent/extproto"
)

func emit(v any) {
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	// os.Stdout.Write is an unbuffered syscall, so each frame leaves the
	// process whole — no explicit flush needed.
	_, _ = os.Stdout.Write(b)
}

func main() {
	emit(map[string]any{"type": "hello", "name": "webext", "version": "0.0.1", "capabilities": []string{"tools", "commands"}})
	emit(map[string]any{"type": "register_tool", "name": "reader_tool", "description": "read-only fetch", "schema": map[string]any{"type": "object"}, "read_only": true})
	emit(map[string]any{"type": "register_tool", "name": "writer_tool", "description": "writes a file", "schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}})
	// One command per response action so the ACP command surface test can drive
	// each translation. `help` deliberately collides with the built-in /help.
	emit(map[string]any{"type": "register_command", "name": "showinfo", "description": "display some info"})
	emit(map[string]any{"type": "register_command", "name": "dowork", "description": "hand the agent a task"})
	emit(map[string]any{"type": "register_command", "name": "fillin", "description": "prefill the input (TUI)"})
	emit(map[string]any{"type": "register_command", "name": "showpanel", "description": "open a panel (TUI)"})
	emit(map[string]any{"type": "register_command", "name": "boom", "description": "fail on purpose"})
	emit(map[string]any{"type": "register_command", "name": "help", "description": "collides with the built-in"})
	// Context contributions so the /context inspector has something to show: a
	// STATIC block (register_context, system guidance) and a live context card.
	emit(map[string]any{"type": "register_context", "text": "always prefer the webext API for fetches"})
	emit(map[string]any{"type": "context_card", "id": "tasks", "label": "open tasks", "text": "task one\ntask two"})
	emit(map[string]any{"type": "ready"})

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), extproto.MaxFrameBytes)
	for sc.Scan() {
		var f struct {
			Type string          `json:"type"`
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			_, _ = os.Stderr.WriteString("EXTSTUB_PARSE_FAIL\n")
			continue
		}
		switch f.Type {
		case "shutdown":
			emit(map[string]any{"type": "shutdown_ack"})
			return
		case "tool_call":
			var a struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(f.Args, &a)
			text := "read-ok"
			if f.Name == "writer_tool" {
				text = "wrote:" + a.Path
			}
			emit(map[string]any{
				"type":     "tool_result",
				"id":       f.ID,
				"content":  []map[string]any{{"type": "text", "text": text}},
				"is_error": false,
			})
		case "command_invoked":
			// For a command_invoked frame the host sends `args` as a JSON string
			// (the trailing text the user typed), so decode it as one. Each
			// command maps to one response action; the args are echoed where it
			// helps the test confirm the round-trip.
			var cmdArgs string
			_ = json.Unmarshal(f.Args, &cmdArgs)
			resp := map[string]any{"type": "command_response", "id": f.ID}
			switch f.Name {
			case "showinfo":
				resp["action"] = "display"
				resp["display"] = "info:" + cmdArgs
			case "dowork":
				resp["action"] = "prompt"
				resp["prompt"] = "do this task: " + cmdArgs
			case "fillin":
				resp["action"] = "insert"
				resp["insert"] = "prefilled:" + cmdArgs
			case "showpanel":
				resp["action"] = "open_panel"
				resp["open_panel"] = map[string]any{
					"id":     "p1",
					"title":  "Panel Title",
					"lines":  []string{"line one", "line two"},
					"footer": "the footer",
				}
			case "boom":
				resp["action"] = "error"
				resp["error"] = "kaboom"
			default:
				// help, or anything unexpected: a display the host should never
				// reach for `help` (the built-in wins).
				resp["action"] = "display"
				resp["display"] = "EXT-HELP-SHOULD-NOT-APPEAR"
			}
			emit(resp)
		}
	}
}
