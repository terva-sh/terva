// Command mcpstub is a deterministic fake MCP server for tests. It
// speaks newline-delimited JSON-RPC 2.0 on stdio and implements
// initialize, tools/list (two tools, paginated when asked with
// --paginate), and tools/call:
//
//	add        -> text content with the sum of args a+b
//	fail       -> isError result
//	imagetool  -> one image content block (1x1 PNG, base64)
//	pingback   -> sends the client server requests first (unlisted)
//
// Flags: --paginate splits tools/list into two pages to exercise the
// cursor loop; --die-after-init exits right after the handshake to
// exercise pending-call failure; --hold-on-eof keeps running after stdin
// closes, so a stop must escalate to a signal; --remote-mark sets the
// terva.sh/remote _meta mark in the initialize result, as terva-mcp-bridge does.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type req struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func reply(id *int64, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(b))
}

var tools = []map[string]any{
	{"name": "add", "description": "add two numbers", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "number"}, "b": map[string]any{"type": "number"}}}},
	{"name": "fail", "description": "always errors", "inputSchema": map[string]any{"type": "object"}},
	{"name": "imagetool", "description": "returns an image", "inputSchema": map[string]any{"type": "object"}},
	{"name": "peek", "description": "read-only lookup", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
}

func main() {
	paginate := false
	dieAfterInit := false
	blockStdin := false
	noReply := false
	closeStdout := false
	holdOnEOF := false
	remoteMark := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--remote-mark":
			remoteMark = true
		case "--hold-on-eof":
			// Ignore stdin EOF, so a stop has to escalate to a signal.
			holdOnEOF = true
		case "--paginate":
			paginate = true
		case "--die-after-init":
			dieAfterInit = true
		case "--block-stdin":
			blockStdin = true
		case "--no-reply":
			noReply = true
		case "--close-stdout":
			closeStdout = true
		}
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		switch r.Method {
		case "initialize":
			res := map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "mcpstub", "version": "0.0.1"},
			}
			if remoteMark {
				res["_meta"] = map[string]any{"terva.sh/remote": true}
			}
			reply(r.ID, res)
		case "notifications/initialized":
			if dieAfterInit {
				os.Exit(0)
			}
		case "tools/list":
			if !paginate {
				reply(r.ID, map[string]any{"tools": tools})
				if closeStdout {
					_ = os.Stdout.Close()
					time.Sleep(time.Hour)
					return
				}
				if blockStdin {
					// The handshake is complete. A byte from the next request
					// proves the host started writing before we stop reading.
					if _, err := os.Stdin.Read(make([]byte, 1)); err != nil {
						os.Exit(2)
					}
					fmt.Fprintln(os.Stderr, "stdin stopped")
					time.Sleep(time.Hour)
					return
				}
				continue
			}
			var p struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(r.Params, &p)
			if p.Cursor == "" {
				reply(r.ID, map[string]any{"tools": tools[:1], "nextCursor": "page2"})
			} else {
				reply(r.ID, map[string]any{"tools": tools[1:]})
			}
		case "tools/call":
			if noReply {
				fmt.Fprintln(os.Stderr, "request read")
				continue
			}
			var p struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(r.Params, &p)
			switch p.Name {
			case "add":
				callAdd(r.ID, p.Args)
			case "fail":
				reply(r.ID, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "it broke"}}})
			case "imagetool":
				// Smallest valid-enough payload; the client only
				// base64-decodes, it does not parse the image.
				reply(r.ID, map[string]any{"content": []map[string]any{{"type": "image", "data": "iVBORw0KGgo=", "mimeType": "image/png"}}})
			case "pingback":
				pingback(sc, r.ID, p.Args)
			default:
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": -32601, "message": "unknown tool"}})
				fmt.Println(string(b))
			}
		}
	}
	if holdOnEOF {
		time.Sleep(time.Hour)
	}
}

func callAdd(id *int64, args json.RawMessage) {
	var a struct{ A, B float64 }
	_ = json.Unmarshal(args, &a)
	reply(id, map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("%g", a.A+a.B)}}})
}

// pingback sends the client server requests before it answers the call: the
// number of pings in args (default 1) under string ids, then an unsupported
// method under the call's own id, which a server's id space can repeat. A
// notification rides along and must get no reply. While it waits for the
// replies it still answers add calls, so a test can interleave request writes
// with reply writes on one pipe. The tool result carries every reply, one per
// line, then any line that did not parse as JSON, prefixed "corrupt: ".
//
// By default it writes every request before it reads stdin: a server that
// floods before it reads. With read_while_sending it reads stdin while it
// writes, as a server that keeps reading does, so a burst never depends on how
// much the platform's pipe buffers.
func pingback(sc *bufio.Scanner, id *int64, args json.RawMessage) {
	var a struct {
		Pings            int  `json:"pings"`
		ReadWhileSending bool `json:"read_while_sending"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Pings < 1 {
		a.Pings = 1
	}
	send := func() {
		for i := 0; i < a.Pings; i++ {
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":\"srv-ping-%d\",\"method\":\"ping\"}\n", i)
		}
		fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"sampling/createMessage\",\"params\":{}}\n", *id)
		fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"hi"}}`)
	}
	var replies, corrupt []string
	collect := func() { collectReplies(sc, a.Pings+1, &replies, &corrupt) }
	if a.ReadWhileSending {
		collected := make(chan struct{})
		go func() { collect(); close(collected) }()
		send()
		<-collected
	} else {
		send()
		collect()
	}
	text := strings.Join(append(replies, corrupt...), "\n")
	reply(id, map[string]any{"content": []map[string]any{{"type": "text", "text": text}}})
}

// collectReplies reads stdin until it holds want replies, and answers any add
// call that arrives meanwhile.
func collectReplies(sc *bufio.Scanner, want int, replies, corrupt *[]string) {
	for len(*replies) < want && sc.Scan() {
		var m struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			*corrupt = append(*corrupt, "corrupt: "+sc.Text())
			continue
		}
		if m.Method == "" {
			*replies = append(*replies, sc.Text())
			continue
		}
		var r req
		var p struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Method == "tools/call" &&
			json.Unmarshal(r.Params, &p) == nil && p.Name == "add" {
			callAdd(r.ID, p.Args)
		}
	}
}
