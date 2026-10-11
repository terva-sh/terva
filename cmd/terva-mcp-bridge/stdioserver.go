package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"terva.sh/terva/packages/provider/lineframe"
)

// bridgeMaxFrameBytes is this bridge's frame ceiling, on BOTH halves of the
// relay. It is larger than lineframe.DefaultMaxBytes because an MCP tool result
// legitimately carries a screenshot or a whole file read, and it lives here as
// one constant because the two halves used to declare it separately — a limit
// written twice is a limit that drifts, and neither copy appeared in
// docs/resource-limits.md's inventory.
const bridgeMaxFrameBytes = 8 << 20

// remoteMetaKey is the _meta key by which the bridge tells terva that every
// call it relays leaves the machine. terva reads it from the initialize result
// and classifies the server's read-only tools as network-read, as it does for
// an in-core http server. packages/agent/mcp holds the reading copy, because
// the bridge takes no dependency on that package.
//
// The terva.sh/ prefix follows the MCP rules for _meta key names, and MCP
// reserves no prefix with those labels.
const remoteMetaKey = "terva.sh/remote"

// markRemote returns the upstream initialize result with the remote mark set
// in its _meta. Every other field and _meta key passes through.
//
// 🔑 The mark is the bridge's statement, not the remote's. Any value the remote
// put at that key, in any letter case, is replaced, so a remote server cannot
// unmark itself through the bridge. The server is marked once rather than each
// tool, because the bridge answers initialize itself, while tools/list is the
// remote's own text and terva decodes its keys without regard to case.
//
// 🚨 A result that is not a JSON object cannot carry the mark, and relaying it
// unmarked would make the remote's read-only tools local reads to terva. The
// bridge refuses to start instead.
func markRemote(initRes json.RawMessage) (json.RawMessage, error) {
	var res map[string]json.RawMessage
	if err := json.Unmarshal(initRes, &res); err != nil || res == nil {
		return nil, fmt.Errorf("the upstream initialize result is not a JSON object, so the bridge cannot mark it as remote")
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(res["_meta"], &meta) != nil || meta == nil {
		meta = map[string]json.RawMessage{}
	}
	for k := range meta {
		if strings.EqualFold(k, remoteMetaKey) {
			delete(meta, k)
		}
	}
	meta[remoteMetaKey] = json.RawMessage(`true`)
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	res["_meta"] = rawMeta
	return json.Marshal(res)
}

// stdioServer is the bridge's downstream face: a newline-delimited JSON-RPC MCP
// server over stdin/stdout, spawned by terva like any stdio MCP command. It
// answers the handshake locally (returning the remote's captured initialize
// result, so terva sees the real server's capabilities, with the remote mark
// added) and RELAYS tools/list and tools/call to the upstream HTTP client 1:1 —
// terva namespaces the tool names itself, so nothing is rewritten there.
// Requests are handled concurrently with a write mutex, matching what terva's
// multiplexing client may send.
type stdioServer struct {
	up      *upstream
	initRes json.RawMessage
	in      io.Reader
	out     io.Writer

	mu sync.Mutex // serialises writes to out
}

// newStdioServer marks initRes here, so no stdio face of the bridge can answer
// initialize without the mark.
func newStdioServer(up *upstream, initRes json.RawMessage, in io.Reader, out io.Writer) (*stdioServer, error) {
	marked, err := markRemote(initRes)
	if err != nil {
		return nil, err
	}
	return &stdioServer{up: up, initRes: marked, in: in, out: out}, nil
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
		// It also gives terva the remote's protocolVersion, so both legs agree on
		// it. The version terva offers here goes nowhere: the bridge sends the
		// remote the version the remote negotiated with the bridge.
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
