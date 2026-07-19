//go:build terva_no_mcp_http

package agent

import (
	"context"
	"strings"
	"testing"
)

// TestApprovalHTTPFailsClosedWithoutTransport: a build compiled with
// terva_no_mcp_http has no HTTP transport, so a well-formed --approval-http
// descriptor still cannot start a carrier — Start fails "not compiled in" and the
// caller keeps the gate's refuse-by-default. The tag must never silently drop the
// approval requirement and let tool calls through unconfirmed.
func TestApprovalHTTPFailsClosedWithoutTransport(t *testing.T) {
	confirmer, stop, err := startHTTPConfirmer(context.Background(), `{"url":"http://127.0.0.1:8/mcp"}`, "")
	if err == nil {
		if stop != nil {
			stop()
		}
		t.Fatalf("without the http transport, the carrier must fail to start, got confirmer %v", confirmer)
	}
	if !strings.Contains(err.Error(), "not compiled in") {
		t.Errorf("error = %q, want it to name the missing http transport", err)
	}
}
