// Command mcpstub is a deterministic fake MCP server for tests. It
// speaks newline-delimited JSON-RPC 2.0 on stdio and implements
// initialize, tools/list (two tools, paginated when asked with
// --paginate), and tools/call:
//
//	add        -> text content with the sum of args a+b
//	fail       -> isError result
//	imagetool  -> one image content block (1x1 PNG, base64)
//
// Flags: --paginate splits tools/list into two pages to exercise the
// cursor loop; --die-after-init exits right after the handshake to
// exercise pending-call failure.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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
	for _, a := range os.Args[1:] {
		switch a {
		case "--paginate":
			paginate = true
		case "--die-after-init":
			dieAfterInit = true
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
			reply(r.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "mcpstub", "version": "0.0.1"},
			})
		case "notifications/initialized":
			if dieAfterInit {
				os.Exit(0)
			}
		case "tools/list":
			if !paginate {
				reply(r.ID, map[string]any{"tools": tools})
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
			var p struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(r.Params, &p)
			switch p.Name {
			case "add":
				var a struct{ A, B float64 }
				_ = json.Unmarshal(p.Args, &a)
				reply(r.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("%g", a.A+a.B)}}})
			case "fail":
				reply(r.ID, map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "it broke"}}})
			case "imagetool":
				// Smallest valid-enough payload; the client only
				// base64-decodes, it does not parse the image.
				reply(r.ID, map[string]any{"content": []map[string]any{{"type": "image", "data": "iVBORw0KGgo=", "mimeType": "image/png"}}})
			default:
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": -32601, "message": "unknown tool"}})
				fmt.Println(string(b))
			}
		}
	}
}
