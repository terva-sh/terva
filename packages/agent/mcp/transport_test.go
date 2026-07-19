package mcp

import (
	"context"
	"strings"
	"testing"
)

// TestServerConfigValidation pins the transport discriminator's rules: a stdio
// server needs a command and no url; an http server needs a url and no command;
// a config mixing the two is refused. These run in every build (validate is
// always compiled).
func TestServerConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		ok   bool
	}{
		{"valid stdio", ServerConfig{Command: "srv"}, true},
		{"valid http", ServerConfig{Transport: "http", URL: "https://x/mcp"}, true},
		{"stdio no command", ServerConfig{}, false},
		{"stdio with url", ServerConfig{Command: "srv", URL: "https://x"}, false},
		{"http no url", ServerConfig{Transport: "http"}, false},
		{"http with command", ServerConfig{Transport: "http", URL: "https://x", Command: "srv"}, false},
	}
	for _, c := range cases {
		err := c.cfg.validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: validate err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// TestUnknownTransportFailsCleanly: a transport kind with no registered factory
// fails with a clear "not compiled in", never a silent fallback to the wrong
// wire. (In a terva_no_mcp_http build, "http" itself is such a kind — see
// mcp_nohttp_test.go.)
func TestUnknownTransportFailsCleanly(t *testing.T) {
	_, err := Start(context.Background(), "x", ServerConfig{Transport: "carrier-pigeon"}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "not compiled in") {
		t.Errorf("unknown transport should fail 'not compiled in', got %v", err)
	}
}
