package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// stubPublisher records what it was asked to publish and answers with a fixed
// record, standing in for the workspace's share store.
type stubPublisher struct {
	calls []struct{ path, name string }
	ref   core.SharedFile
	err   error
}

func (p *stubPublisher) Publish(_ context.Context, path, name string) (core.SharedFile, error) {
	p.calls = append(p.calls, struct{ path, name string }{path, name})
	if p.err != nil {
		return core.SharedFile{}, p.err
	}
	return p.ref, nil
}

func shareArgs(t *testing.T, a map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestShareFilePublishesAndTellsTheModelSoWithoutAHandle(t *testing.T) {
	cwd := testsupport.TempDir(t)
	pub := &stubPublisher{ref: core.SharedFile{
		ID: "shr_abc", Name: "report.pdf", Kind: "document", Mime: "application/pdf", Size: 2048,
	}}
	tool := &ShareFileTool{CWD: cwd, Sandbox: NewSandbox(cwd), Publisher: pub}

	res, err := tool.Execute(context.Background(), shareArgs(t, map[string]any{"path": "out/report.pdf"}), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("result is an error: %s", resultText(res))
	}
	// Relative paths resolve against the session cwd, like every other tool.
	if want := filepath.Join(cwd, "out", "report.pdf"); pub.calls[0].path != want {
		t.Errorf("published %q, want %q", pub.calls[0].path, want)
	}
	if len(res.Shared) != 1 || res.Shared[0].ID != "shr_abc" {
		t.Fatalf("Shared = %+v, want the published record", res.Shared)
	}

	text := resultText(res)
	if !strings.Contains(text, "report.pdf") {
		t.Errorf("model text %q does not name the file", text)
	}
	// The id is the retrievable handle. It belongs in the record the client
	// reads, never in the transcript the model carries forward.
	if strings.Contains(text, "shr_abc") {
		t.Errorf("model text leaks the share id: %q", text)
	}
}

func TestShareFileCarriesTheCaptionOnTheRecordOnly(t *testing.T) {
	cwd := testsupport.TempDir(t)
	pub := &stubPublisher{ref: core.SharedFile{ID: "shr_a", Name: "chart.png", Kind: "image"}}
	tool := &ShareFileTool{CWD: cwd, Sandbox: NewSandbox(cwd), Publisher: pub}

	res, err := tool.Execute(context.Background(), shareArgs(t, map[string]any{
		"path": "chart.png", "caption": "week 32 latency",
	}), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Shared[0].Caption != "week 32 latency" {
		t.Errorf("Caption = %q, want it on the record", res.Shared[0].Caption)
	}
}

// name relabels the file for the user and is passed through verbatim — the
// store, not the tool, decides what is safe to put on disk.
func TestShareFilePassesTheRelabelThrough(t *testing.T) {
	cwd := testsupport.TempDir(t)
	pub := &stubPublisher{ref: core.SharedFile{ID: "shr_a", Name: "filters.xml"}}
	tool := &ShareFileTool{CWD: cwd, Sandbox: NewSandbox(cwd), Publisher: pub}

	if _, err := tool.Execute(context.Background(), shareArgs(t, map[string]any{
		"path": "tmp0001", "name": "filters.xml",
	}), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pub.calls[0].name != "filters.xml" {
		t.Errorf("relabel = %q, want filters.xml", pub.calls[0].name)
	}
}

// A jailed agent can share what it could already read, and nothing else. The
// gate is the sandbox's read check, so the answer tracks the read-only roots
// rather than being a second, drifting list.
func TestShareFileRefusesWhatTheJailWouldNotLetItRead(t *testing.T) {
	cwd := testsupport.TempDir(t)
	outside := filepath.Join(testsupport.TempDir(t), "secrets.env")
	if err := os.WriteFile(outside, []byte("TOKEN=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := NewSandbox(cwd)
	sb.Lock()
	pub := &stubPublisher{ref: core.SharedFile{ID: "shr_a"}}
	tool := &ShareFileTool{CWD: cwd, Sandbox: sb, Publisher: pub}

	if _, err := tool.Execute(context.Background(), shareArgs(t, map[string]any{"path": outside}), nil); err == nil {
		t.Fatal("Execute(outside the jail) succeeded, want a sandbox refusal")
	}
	if len(pub.calls) != 0 {
		t.Errorf("the publisher was called %d times for a refused path, want 0", len(pub.calls))
	}
}

// A store refusal (a directory, a missing file, over the size cap) reaches the
// model as an error it can act on rather than a silent success.
func TestShareFileSurfacesAStoreRefusal(t *testing.T) {
	cwd := testsupport.TempDir(t)
	pub := &stubPublisher{err: errors.New("file exceeds the size limit")}
	tool := &ShareFileTool{CWD: cwd, Sandbox: NewSandbox(cwd), Publisher: pub}

	_, err := tool.Execute(context.Background(), shareArgs(t, map[string]any{"path": "huge.bin"}), nil)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("Execute error = %v, want the store's refusal", err)
	}
}

// A host with nowhere to share leaves the tool without a publisher. The turn
// should survive that: the model reports it and moves on.
func TestShareFileWithoutAPublisherIsAToolErrorNotAFailedTurn(t *testing.T) {
	cwd := testsupport.TempDir(t)
	tool := &ShareFileTool{CWD: cwd, Sandbox: NewSandbox(cwd)}

	res, err := tool.Execute(context.Background(), shareArgs(t, map[string]any{"path": "a.txt"}), nil)
	if err != nil {
		t.Fatalf("Execute returned a hard error: %v", err)
	}
	if !res.IsError {
		t.Errorf("result = %+v, want IsError", res)
	}
	if len(res.Shared) != 0 {
		t.Errorf("Shared = %+v, want nothing published", res.Shared)
	}
}

func TestShareFileRequiresAPath(t *testing.T) {
	cwd := testsupport.TempDir(t)
	tool := &ShareFileTool{CWD: cwd, Sandbox: NewSandbox(cwd), Publisher: &stubPublisher{}}

	if _, err := tool.Execute(context.Background(), shareArgs(t, map[string]any{}), nil); err == nil {
		t.Fatal("Execute with no path succeeded, want an error")
	}
}
