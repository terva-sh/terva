package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"terva.sh/terva/packages/lineframe"
)

// bridgeMaxFrameBytes is this bridge's frame ceiling, on BOTH halves of the
// relay. It is larger than lineframe.DefaultMaxBytes because an MCP tool result
// legitimately carries a screenshot or a whole file read, and it lives here as
// one constant because the two halves used to declare it separately — a limit
// written twice is a limit that drifts, and neither copy appeared in
// docs/resource-limits.md's inventory.
const bridgeMaxFrameBytes = 8 << 20

// stdioServer is the bridge's downstream face: a newline-delimited JSON-RPC MCP
// server over stdin/stdout, spawned by terva like any stdio MCP command. It
// answers the handshake locally (returning the remote's captured initialize
// result, so terva sees the real server's capabilities) and RELAYS tools/list and
// tools/call to the upstream HTTP client 1:1 — terva namespaces the tool names
// itself, so nothing is rewritten here. Requests are handled concurrently with a
// write mutex, matching what terva's multiplexing client may send.
type stdioServer struct {
	up      *upstream
	initRes json.RawMessage
	in      io.Reader
	out     io.Writer

	mu sync.Mutex // serialises writes to out
}

func newStdioServer(up *upstream, initRes json.RawMessage, in io.Reader, out io.Writer) *stdioServer {
	return &stdioServer{up: up, initRes: initRes, in: in, out: out}
}

func (s *stdioServer) serve(ctx context.Context) error {
	// lineframe, not bufio.Scanner. A single request over the ceiling made
	// Scan() return false and ended serve() outright — so the bridge process
	// exited and every remaining tool call to that server failed for the rest of
	// the session. RECOVER: this wire multiplexes every call to one MCP server,
	// and one oversized argument list must not take the others with it.
	//
	// The ceiling is stated once and shared with the upstream half, which used
	// to carry its own copy of the same number.
	fr := lineframe.NewReader(s.in, bridgeMaxFrameBytes, func(msg string) {
		fmt.Fprintf(os.Stderr, "terva-mcp-bridge: %s\n", msg)
	})
	var wg sync.WaitGroup
	for {
		line, err := fr.Read()
		if err != nil {
			wg.Wait()
			if err == io.EOF {
				return nil
			}
			return err
		}
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if json.Unmarshal(line, &req) != nil {
			continue // not a JSON-RPC frame; skip
		}
		// Notifications (no id) expect no reply. initialized / cancelled are the
		// ones terva sends; both are no-ops for a stateless tools proxy.
		if len(req.ID) == 0 {
			continue
		}
		wg.Add(1)
		go func(req rpcRequest) {
			defer wg.Done()
			s.handle(ctx, req)
		}(req)
	}
}

func (s *stdioServer) handle(ctx context.Context, req rpcRequest) {
	switch req.Method {
	case "initialize":
		// The handshake is answered locally: the upstream was already initialized
		// at startup, and terva just needs a valid initialize result. Returning the
		// remote's own result advertises its real capabilities and server info.
		s.write(resultResponse(req.ID, s.initRes))
	case "ping":
		s.write(resultResponse(req.ID, json.RawMessage(`{}`)))
	case "tools/list", "tools/call":
		res, err := s.up.call(ctx, req.ID, req.Method, req.Params)
		if err != nil {
			s.write(errorResponse(req.ID, -32000, err.Error()))
			return
		}
		s.write(resultResponse(req.ID, res))
	default:
		// Resources/prompts/sampling are out of scope for this tools-only bridge,
		// as they are for core's client — report method-not-found cleanly.
		s.write(errorResponse(req.ID, -32601, "method not supported by terva-mcp-bridge: "+req.Method))
	}
}

func (s *stdioServer) write(frame []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(append(frame, '\n'))
}
