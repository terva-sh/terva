// Command faketerva is a stand-in for `terva rpc` used by the worker runner
// test. It speaks the rpc wire (packages/agent/rpc.go): reads NDJSON `prompt`
// commands on stdin and, for each, emits the response ack, a turn_start, an
// assistant_message echoing the prompt, a `usage` event carrying the running
// cumulative cost (the wire's cost carrier — there is none on `done`), and the
// terminal `done`. It exits on stdin EOF.
//
// It is deliberately NOT terva: the runner test proves the SUPERVISION bridge
// (spawn, translate response/done frames, steer, shutdown) without a real model
// or credential. Conformance against the real `terva rpc` is a separate, live
// concern.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	cumCost := 0.0
	for sc.Scan() {
		var cmd struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &cmd); err != nil || cmd.Type != "prompt" {
			continue
		}
		// The rpc `response` ack — a protocol frame the translator must DROP.
		emit(map[string]any{"type": "response", "command": "prompt", "success": true, "data": map[string]any{"started": true}})
		// core.WireEvent stream — passed through the translator untouched.
		emit(map[string]any{"type": "turn_start"})
		emit(map[string]any{
			"type": "assistant_message",
			"message": map[string]any{
				"role":    "assistant",
				"content": []map[string]any{{"type": "text", "text": "echo: " + cmd.Message}},
			},
		})
		// The cost carrier: a real `terva rpc` emits a usage event per turn with
		// the CUMULATIVE session total, which the supervisor records as live spend.
		// Climb it each prompt so the test can prove last-wins, not a per-turn sum.
		cumCost += 0.0025
		emit(map[string]any{
			"type":       "usage",
			"usage":      map[string]any{"cost_usd": 0.0025},
			"cumulative": map[string]any{"cost_usd": cumCost, "input": 100, "output": 40},
		})
		// The per-prompt terminal — the translator synthesizes task_end from it.
		emit(map[string]any{"type": "done"})
	}
}

func emit(ev map[string]any) {
	if b, err := json.Marshal(ev); err == nil {
		fmt.Fprintln(os.Stdout, string(b))
	}
}
