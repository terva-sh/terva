//go:build terva_no_mcp_http

package mcp

import (
	"context"
	"strings"
	"testing"
)

// TestHTTPNotCompiledIn is the tag-off counterpart: when built without the http
// transport, an "http" server config fails cleanly with "not compiled in" rather
// than silently doing nothing — the graceful degrade the build tag promises.
func TestHTTPNotCompiledIn(t *testing.T) {
	_, err := Start(context.Background(), "remote", ServerConfig{Transport: "http", URL: "https://x/mcp"}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "not compiled in") {
		t.Errorf("http without the tag should fail 'not compiled in', got %v", err)
	}
}
