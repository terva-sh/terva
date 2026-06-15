//go:build terva_acp

package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseMCPServersMapsStdioSkipsNonStdio is verification (a): the ACP
// McpServer union → mcp.ServerConfig mapping. stdio entries map (with the
// env [{name,value}] list flattened to a map); http/sse entries are skipped
// with a warning; an unnamed/commandless stdio entry is skipped.
func TestParseMCPServersMapsStdioSkipsNonStdio(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"alpha","command":"/bin/alpha","args":["--flag","x"],"env":[{"name":"TOKEN","value":"abc"},{"name":"DEBUG","value":"1"}]},
		{"type":"stdio","name":"beta","command":"beta-bin"},
		{"type":"http","name":"webby","url":"https://example.test/mcp"},
		{"type":"sse","name":"streamy","url":"https://example.test/sse"},
		{"name":"noscmd"},
		{"command":"/bin/anon"}
	]`)

	servers, warnings := ParseMCPServers(raw)

	if len(servers) != 2 {
		t.Fatalf("got %d stdio servers; want 2 (alpha, beta). servers=%+v", len(servers), servers)
	}

	// alpha: command, args, and the env list flattened to a map.
	a := servers[0]
	if a.Name != "alpha" || a.Command != "/bin/alpha" {
		t.Errorf("alpha = {%q,%q}; want {alpha,/bin/alpha}", a.Name, a.Command)
	}
	if len(a.Args) != 2 || a.Args[0] != "--flag" || a.Args[1] != "x" {
		t.Errorf("alpha args = %v; want [--flag x]", a.Args)
	}
	if a.Env["TOKEN"] != "abc" || a.Env["DEBUG"] != "1" || len(a.Env) != 2 {
		t.Errorf("alpha env = %v; want {TOKEN:abc, DEBUG:1}", a.Env)
	}

	// beta: explicit type:"stdio", no env.
	b := servers[1]
	if b.Name != "beta" || b.Command != "beta-bin" {
		t.Errorf("beta = {%q,%q}; want {beta,beta-bin}", b.Name, b.Command)
	}
	if b.Env != nil {
		t.Errorf("beta env = %v; want nil (no env entries)", b.Env)
	}

	// Two non-stdio (http, sse) + two malformed-stdio (no name / no command)
	// = four warnings, each naming what was skipped.
	if len(warnings) != 4 {
		t.Fatalf("got %d warnings; want 4. warnings=%v", len(warnings), warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"webby", "http", "streamy", "sse"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q: %v", want, warnings)
		}
	}
	if !strings.Contains(joined, "missing name") {
		t.Errorf("warnings missing the no-name skip: %v", warnings)
	}
	if !strings.Contains(joined, "missing command") {
		t.Errorf("warnings missing the no-command skip: %v", warnings)
	}
}

// TestParseMCPServersEmpty: nil / empty / "[]" payloads yield nothing, no
// warnings — the common case where the editor sends no MCP servers.
func TestParseMCPServersEmpty(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`[]`)} {
		servers, warnings := ParseMCPServers(raw)
		if len(servers) != 0 || len(warnings) != 0 {
			t.Errorf("ParseMCPServers(%q) = (%v, %v); want empty", string(raw), servers, warnings)
		}
	}
}

// TestParseMCPServersMalformed: a non-array / malformed payload is a single
// warning and no servers (best-effort: one bad payload never fails the
// session).
func TestParseMCPServersMalformed(t *testing.T) {
	servers, warnings := ParseMCPServers(json.RawMessage(`{"not":"an array"}`))
	if len(servers) != 0 {
		t.Errorf("got %d servers from malformed payload; want 0", len(servers))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "malformed") {
		t.Errorf("warnings = %v; want one 'malformed' warning", warnings)
	}
}

// TestParseMCPServersDropsBlankEnvName: an env entry with an empty name is
// dropped rather than producing a "" key.
func TestParseMCPServersDropsBlankEnvName(t *testing.T) {
	raw := json.RawMessage(`[{"name":"s","command":"c","env":[{"name":"","value":"x"},{"name":"OK","value":"y"}]}]`)
	servers, _ := ParseMCPServers(raw)
	if len(servers) != 1 {
		t.Fatalf("got %d servers; want 1", len(servers))
	}
	if _, blank := servers[0].Env[""]; blank {
		t.Error("blank-named env var should be dropped")
	}
	if servers[0].Env["OK"] != "y" {
		t.Errorf("env = %v; want OK:y", servers[0].Env)
	}
}
