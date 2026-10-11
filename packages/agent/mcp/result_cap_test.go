//go:build !terva_no_mcp_http

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/resultcap"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// startCapFixture runs a fake HTTP MCP server named "fixture" that lists
// toolsList and answers every tools/call with callResult, both raw JSON.
func startCapFixture(t *testing.T, toolsList, callResult string) *Manager {
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
		switch req.Method {
		case "tools/list":
			result = toolsList
		case "tools/call":
			result = callResult
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, result)
	}))
	t.Cleanup(server.Close)
	m := StartAll(context.Background(), &Config{Servers: map[string]ServerConfig{"fixture": {Transport: "http", URL: server.URL}}}, "", nil)
	t.Cleanup(m.StopAll)
	return m
}

// useSpillDir points os.TempDir at a fresh directory so a spill is visible to
// the test and removed with it.
func useSpillDir(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	t.Setenv("TMPDIR", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", dir)
		t.Setenv("TEMP", dir)
	}
	return dir
}

// callFixture calls the one tool of a fixture whose every call returns
// content, a raw JSON content array, and returns the result's blocks and
// details.
func callFixture(t *testing.T, content string) ([]provider.Content, map[string]any) {
	t.Helper()
	m := startCapFixture(t, `{"tools":[{"name":"dump","description":"dumps","inputSchema":{"type":"object"}}]}`,
		`{"content":`+content+`,"isError":false}`)
	infos := m.Tools()
	if len(infos) != 1 {
		t.Fatalf("fixture listed %d tools: %v", len(infos), m.Warnings())
	}
	res, err := m.NewTool(infos[0]).Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	details, _ := res.Details.(map[string]any)
	return res.Content, details
}

func TestMCPTextResultOverTheCapSpills(t *testing.T) {
	dir := useSpillDir(t)
	big := strings.Repeat("0123456789abcdef", 3*resultcap.MaxBytes/16) // 150 KiB
	png := []byte{0x89, 'P', 'N', 'G'}
	content, _ := json.Marshal([]map[string]string{
		{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(png)},
		{"type": "text", "text": big},
	})
	blocks, details := callFixture(t, string(content))

	if len(blocks) != 3 {
		t.Fatalf("want image, cut text, pointer; got %d blocks", len(blocks))
	}
	if ib, ok := blocks[0].(provider.ImageBlock); !ok || ib.MimeType != "image/png" || string(ib.Data) != string(png) {
		t.Fatalf("image changed: %#v", blocks[0])
	}
	if kept := blocks[1].(provider.TextBlock).Text; len(kept) != resultcap.MaxBytes || !strings.HasPrefix(big, kept) {
		t.Fatalf("inline text is %d bytes, want the first %d", len(kept), resultcap.MaxBytes)
	}
	path, _ := details["full_output_path"].(string)
	if path == "" || filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "terva-mcp-") {
		t.Fatalf("full_output_path = %q, want terva-mcp-<hex>.log in %s", path, dir)
	}
	pointer := blocks[2].(provider.TextBlock).Text
	if !strings.Contains(pointer, "\nfull output: "+path+"\n") || !strings.Contains(pointer, "Read this file with offset and limit") {
		t.Fatalf("pointer does not send the model to the file: %q", pointer)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != big {
		t.Fatalf("spill holds %d bytes (%v), want the full %d", len(got), err, len(big))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("spill file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestMCPTextResultUnderTheCapIsUnchanged(t *testing.T) {
	dir := useSpillDir(t)
	text := strings.Repeat("y", resultcap.MaxBytes)
	content, _ := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	blocks, details := callFixture(t, string(content))
	if len(blocks) != 1 || blocks[0].(provider.TextBlock).Text != text {
		t.Fatalf("a result at the limit changed: %d blocks", len(blocks))
	}
	if details != nil {
		t.Fatalf("a result at the limit carries details %v", details)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a result at the limit wrote %d file(s)", len(entries))
	}
}
