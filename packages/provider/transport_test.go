package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// codexSSEServer serves a minimal completed response with identity headers,
// the way chatgpt.com fronted by Cloudflare does.
func codexSSEServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req_abc123")
		w.Header().Set("cf-ray", "8f2d1c-SJC")
		w.Header().Set("openai-processing-ms", "1234")
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// drain consumes a stream to completion so its connection returns to the pool
// — which is exactly what production does, and what makes the SECOND dispatch
// the interesting one.
func drainEvents(t *testing.T, ch <-chan Event) (transport *EventTransport) {
	t.Helper()
	for ev := range ch {
		if tr, ok := ev.(EventTransport); ok {
			trr := tr
			transport = &trr
		}
	}
	return transport
}

// The forensics contract: every dispatch reports how it reached the provider
// — fresh dial vs reused connection, plus the response's identity headers —
// BEFORE any content event, so the row lands next to the usage row it
// explains. The second dispatch on one client must report ConnReused=true:
// that is the healthy baseline the cache investigation reads collapses
// against, and if pooling ever breaks, this assertion is the tripwire.
func TestCodexStreamReportsTransport(t *testing.T) {
	srv := codexSSEServer(t)
	c := NewOpenAICodex("token", "acct", srv.URL).(*codexClient)
	msgs := []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}}

	first := streamOnce(t, c, msgs)
	if first == nil {
		t.Fatal("first dispatch emitted no EventTransport")
	}
	if first.Info.ConnReused {
		t.Error("first dispatch reported a reused connection before any existed")
	}
	if first.Info.RequestID != "req_abc123" {
		t.Errorf("RequestID = %q, want req_abc123", first.Info.RequestID)
	}
	if first.Info.Ray != "8f2d1c-SJC" {
		t.Errorf("Ray = %q, want 8f2d1c-SJC", first.Info.Ray)
	}
	if first.Info.ProcessingMS != 1234 {
		t.Errorf("ProcessingMS = %d, want 1234", first.Info.ProcessingMS)
	}
	if first.Info.RemoteAddr == "" {
		t.Error("RemoteAddr is empty — the trace never saw the connection")
	}
	if first.Info.Proto == "" {
		t.Error("Proto is empty")
	}

	second := streamOnce(t, c, msgs)
	if second == nil {
		t.Fatal("second dispatch emitted no EventTransport")
	}
	if !second.Info.ConnReused {
		t.Error("second dispatch re-dialed — connection pooling is broken, which is the exact churn the forensics exist to catch")
	}
	if second.Info.RemoteAddr != first.Info.RemoteAddr {
		t.Errorf("reused connection changed peers: %q -> %q", first.Info.RemoteAddr, second.Info.RemoteAddr)
	}
}

func streamOnce(t *testing.T, c *codexClient, msgs []Message) *EventTransport {
	t.Helper()
	ch, err := c.Stream(context.Background(), Request{Model: "gpt-5.5", Messages: msgs})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return drainEvents(t, ch)
}
