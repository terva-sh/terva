// Command fakeclaude is a stand-in for `claude -p --input-format stream-json`
// used by the worker runner test. It speaks the same wire the real translator
// was captured against (packages/agent/worker/testdata/claude-2.1.209-stream.jsonl):
//
//   - emits a system/init event on start (carrying a version stamp);
//   - reads stream-json user frames on stdin and echoes each back as an
//     assistant message ("echo: <text>"), so a steer round-trips visibly;
//   - on stdin EOF (the runner's graceful shutdown), emits a result envelope
//     with a cost and exits 0.
//
// It is deliberately NOT the real CLI: the runner test proves the SUPERVISION
// bridge (spawn, translate, steer, raw retention, shutdown), which must hold
// without spending a real inference call. The translator itself is pinned
// separately against the captured real stream in claude_test.go.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	emit(map[string]any{
		"type":                "system",
		"subtype":             "init",
		"claude_code_version": "9.9.9-fake",
		"model":               "fake-model",
		"session_id":          "fake-session",
	})

	turns := 0
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var frame struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &frame); err != nil || frame.Type != "user" {
			continue
		}
		var text string
		for _, c := range frame.Message.Content {
			if c.Type == "text" {
				text = c.Text
				break
			}
		}
		turns++
		emit(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"role":    "assistant",
				"content": []map[string]any{{"type": "text", "text": "echo: " + text}},
			},
		})
	}

	// stdin closed -> the runner asked us to drain. Report a terminal result the
	// translator turns into task_end, with a cost off the envelope.
	emit(map[string]any{
		"type":           "result",
		"subtype":        "success",
		"is_error":       false,
		"total_cost_usd": 0.02,
		"num_turns":      turns,
		"duration_ms":    7,
		"usage":          map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
}

func emit(ev map[string]any) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	// Write directly to the file so each event is flushed immediately; a
	// buffered writer would hold events until exit and defeat the round-trip.
	fmt.Fprintln(os.Stdout, string(b))
}
