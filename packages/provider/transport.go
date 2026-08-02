package provider

import (
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
)

// Transport forensics for the provider-side cache mystery. A measured codex
// session re-read 14.4M prompt tokens across two runs of consecutive
// full-price dispatches while terva's outgoing bytes were provably
// append-only — so the remaining suspects live BELOW the request body: which
// connection the dispatch rode, which edge it landed on, which backend served
// it. None of that was recorded, which is why the investigation stalled at
// "provider-side". This file records it.
//
// The hypothesis this data can settle: provider cache routing is
// connection- or edge-affine, so a dispatch that reuses the previous
// dispatch's connection hits the same cache and a dispatch on a fresh
// connection (or a different edge colo) lands cold. If the floor-pinned
// runs correlate with ConnReused=false or a changed Ray colo, the root
// cause is transport churn and the fix is transport-shaped. If they do not,
// connection affinity is eliminated the same way the prefix was.

// TransportInfo describes how one dispatch physically reached the provider.
type TransportInfo struct {
	// ConnReused is true when the request rode an existing keep-alive
	// connection; false means a fresh dial (new TLS session, new edge
	// assignment).
	ConnReused bool `json:"conn_reused"`
	// RemoteAddr is the peer the connection terminates at — for a fronted
	// endpoint this is the CDN edge, so a change here means a re-dial landed
	// somewhere else.
	RemoteAddr string `json:"remote_addr,omitempty"`
	// Proto is the negotiated protocol from the response (HTTP/2.0 vs
	// HTTP/1.1). H2 multiplexes every dispatch over one connection; H1 pools
	// per-host and can churn.
	Proto string `json:"proto,omitempty"`
	// RequestID is the provider's x-request-id response header, the handle a
	// provider-side investigation needs to look anything up.
	RequestID string `json:"request_id,omitempty"`
	// Ray is the CDN trace header (cf-ray for Cloudflare-fronted endpoints);
	// its suffix names the edge colo that served the request.
	Ray string `json:"ray,omitempty"`
	// ProcessingMS is the provider's own openai-processing-ms figure, when
	// present. A cache miss re-reads the whole prompt, so this tends to jump
	// with one.
	ProcessingMS int64 `json:"processing_ms,omitempty"`
}

// EventTransport reports the transport picture of the dispatch whose events
// follow it. Emitted once per successful Stream, before any content event.
type EventTransport struct {
	Info TransportInfo
}

func (EventTransport) isEvent() {}

// transportCapture accumulates httptrace callbacks for one Stream call.
// Retries overwrite it in place — attempts are sequential, and the attempt
// that produced the returned response is the last one to have written — but
// the trace callbacks themselves may race the reader on an abandoned
// attempt, so every access takes the lock.
type transportCapture struct {
	mu     sync.Mutex
	reused bool
	addr   string
}

// trace returns the ClientTrace to install on a request attempt.
func (tc *transportCapture) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(ci httptrace.GotConnInfo) {
			tc.mu.Lock()
			tc.reused = ci.Reused
			if ci.Conn != nil {
				tc.addr = ci.Conn.RemoteAddr().String()
			}
			tc.mu.Unlock()
		},
	}
}

// info folds the captured trace and the response's identity headers into the
// dispatch's TransportInfo.
func (tc *transportCapture) info(resp *http.Response) TransportInfo {
	tc.mu.Lock()
	ti := TransportInfo{ConnReused: tc.reused, RemoteAddr: tc.addr}
	tc.mu.Unlock()
	ti.Proto = resp.Proto
	ti.RequestID = resp.Header.Get("x-request-id")
	// cf-ray on Cloudflare-fronted endpoints (chatgpt.com); x-amzn-trace-id
	// and friends can join here if another provider's mystery needs them.
	ti.Ray = resp.Header.Get("cf-ray")
	if ms := resp.Header.Get("openai-processing-ms"); ms != "" {
		if v, err := strconv.ParseInt(ms, 10, 64); err == nil {
			ti.ProcessingMS = v
		}
	}
	return ti
}
