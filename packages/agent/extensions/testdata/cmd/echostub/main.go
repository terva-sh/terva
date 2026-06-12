// Command echostub is a deterministic test extension used by
// TestConcurrentLargeFramesNoInterleave to verify that the host
// serializes its stdin writes (no interleaved/corrupted frames under
// concurrent InvokeTool calls).
//
// It is written in Go rather than a shell+python stub on purpose: the
// earlier python stubs introduced their own stdin read-ahead / per-spawn
// timing variance that occasionally left a tool_call unanswered, which
// looked like a host bug but wasn't. A single compiled binary reading
// with bufio.Scanner and writing whole frames has zero interpreter
// buffering surprises, so a missing reply here means a genuine host-side
// frame corruption — exactly what the test is meant to catch.
//
// Protocol: emit hello + register_tool(bigtool) + ready, then for each
// tool_call reply with a tool_result whose text is the byte length of
// args.payload. A line that fails to parse is an unrecoverable
// (interleaved) frame whose id is unknown, so it cannot be answered; we
// note it on stderr (captured to the extension log) so a real corruption
// is diagnosable instead of a bare timeout.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
)

func emit(v any) {
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	// os.Stdout.Write is an unbuffered syscall, so each frame leaves the
	// process whole — no explicit flush needed.
	_, _ = os.Stdout.Write(b)
}

func main() {
	emit(map[string]any{"type": "hello", "name": "echo", "version": "0.0.1", "capabilities": []string{"tools"}})
	emit(map[string]any{"type": "register_tool", "name": "bigtool", "description": "echo length", "schema": map[string]any{}})
	emit(map[string]any{"type": "ready"})

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var f struct {
			Type string          `json:"type"`
			ID   string          `json:"id"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			_, _ = os.Stderr.WriteString("ECHOSTUB_PARSE_FAIL\n")
			continue
		}
		switch f.Type {
		case "shutdown":
			emit(map[string]any{"type": "shutdown_ack"})
			return
		case "tool_call":
			var a struct {
				Payload string `json:"payload"`
			}
			_ = json.Unmarshal(f.Args, &a)
			emit(map[string]any{
				"type":     "tool_result",
				"id":       f.ID,
				"content":  []map[string]any{{"type": "text", "text": strconv.Itoa(len(a.Payload))}},
				"is_error": false,
			})
		}
	}
}
