//go:build !terva_no_mcp_http

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func executeStructuredMCPFixture(t *testing.T, payload string, isError bool) (core.ToolResult, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.ID == nil {
			w.WriteHeader(202)
			return
		}
		result := resultFor(req.Method)
		if req.Method == "tools/call" {
			result = fmt.Sprintf(`{"content":[{"type":"text","text":"display"}],"structuredContent":%s,"isError":%t}`, payload, isError)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, result)
	}))
	defer server.Close()
	m := StartAll(context.Background(), &Config{Servers: map[string]ServerConfig{"fixture": {Transport: "http", URL: server.URL}}}, "", nil)
	defer m.StopAll()
	infos := m.Tools()
	if len(infos) != 1 {
		t.Fatalf("fixture did not provide tool: %v", m.Warnings())
	}
	return m.NewTool(infos[0]).Execute(context.Background(), json.RawMessage(`{}`), nil)
}

func TestMCPStructuredContentReachesCoreTool(t *testing.T) {
	res, err := executeStructuredMCPFixture(t, `{"items":[{"id":7}]}`, true)
	if err != nil || string(res.StructuredContent) != `{"items":[{"id":7}]}` || !res.IsError || len(res.Content) != 1 {
		t.Fatalf("structured content lost: %+v, %v", res, err)
	}
}

func TestMCPStructuredContentBoundedBeforeCoreResult(t *testing.T) {
	res, err := executeStructuredMCPFixture(t, `{"blob":"`+strings.Repeat("x", 1<<20)+`"}`, false)
	if err != nil || res.IsError || !res.StructuredContentOmitted || len(res.StructuredContent) != 0 {
		t.Fatalf("oversized MCP output retained or text call failed: bytes=%d, %v", len(res.StructuredContent), err)
	}
	if len(res.Content) != 1 || res.Content[0].(provider.TextBlock).Text != "display" {
		t.Fatal("oversized structured field discarded usable display content")
	}
}
