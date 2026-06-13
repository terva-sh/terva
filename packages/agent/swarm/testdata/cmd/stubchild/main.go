// stubchild is a minimal swarm-agent stand-in used by the runner
// end-to-end test. It speaks the daemon protocol the real terva
// binary will implement next:
//
//   - parses --swarm-agent <path>, --session <path>, --cwd <path>,
//     and an optional positional task,
//   - opens a unix-socket listener at the inbox path so the
//     supervisor's Inbox can dial through,
//   - emits well-formed JSONL events on stdout that the supervisor
//     mirrors into the durable event log,
//   - reads one line per supervisor message and echoes it back as
//     a "user_message" event followed by a fake "assistant_message"
//     so the runner sees the dialogue happen.
//
// The runner test compiles this binary into a tempdir, points
// swarm's execRunner at it via Command, and asserts the events
// flow through correctly without needing the full terva model
// machinery.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/swarm"
)

func main() {
	var (
		inboxPath   string
		sessionPath string
		cwd         string
	)
	flag.StringVar(&inboxPath, "swarm-agent", "", "inbox socket path")
	flag.StringVar(&sessionPath, "session", "", "session file path")
	flag.StringVar(&cwd, "cwd", "", "working directory")
	flag.Parse()

	if inboxPath == "" {
		fmt.Fprintln(os.Stderr, "stubchild: --swarm-agent required")
		os.Exit(2)
	}

	emit := newEmitter()

	// Open the inbox listener BEFORE doing any work so the supervisor
	// can dial through as soon as it sees the agent_ready event. If we
	// processed the initial task first, the parent's SendInput call
	// would race the stub's net.Listen and trip ErrNotReady.
	ln, err := net.Listen("unix", inboxPath)
	if err != nil {
		emit("error", map[string]any{"message": err.Error()})
		os.Exit(1)
	}
	defer os.Remove(inboxPath)

	emit("agent_ready", map[string]any{
		"inbox":   inboxPath,
		"session": sessionPath,
		"cwd":     cwd,
	})

	// Initial task lives in the positional. Process it as the first
	// user turn so the supervisor's "initial task" path is exercised.
	if task := flag.Arg(0); task != "" {
		runTurn(emit, task, 0)
	}

	turn := 1
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				// Share the real parser so the stub speaks the same
				// (JSON-or-legacy) inbox protocol as a real child.
				kind, text := swarm.ParseInboxLine(trimNL(line))
				switch kind {
				case "shutdown":
					emit("agent_stopped", map[string]any{"reason": "shutdown"})
					_ = c.Close()
					return
				case "cancel":
					emit("turn_end", map[string]any{"stop": "cancelled"})
				case "user":
					runTurn(emit, text, turn)
					turn++
				}
			}
			if err != nil {
				_ = c.Close()
				break
			}
		}
	}
}

// runTurn fakes one model round-trip: turn_start, an echoed
// user_message, an assistant_message that echoes back, and a
// turn_end. Enough event variety that applyEventToSink has
// something to interpret.
func runTurn(emit emitter, text string, step int) {
	emit("turn_start", map[string]any{"step": step})
	emit("user_message", map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
	})
	emit("assistant_message", map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "echo: " + text},
		},
	})
	emit("turn_end", map[string]any{"stop": "end"})
}

type emitter = func(string, map[string]any)

func newEmitter() emitter {
	var mu sync.Mutex
	enc := json.NewEncoder(os.Stdout)
	return func(typ string, data map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		if data == nil {
			data = map[string]any{}
		}
		data["type"] = typ
		data["time"] = time.Now().Format(time.RFC3339Nano)
		_ = enc.Encode(data)
	}
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
