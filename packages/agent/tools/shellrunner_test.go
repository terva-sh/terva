package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

type fakeRunner struct {
	got   ShellRequest
	exit  int
	err   error
	write func(ShellRequest)
}

func (f *fakeRunner) ShellName() string { return "fake-sh" }
func (f *fakeRunner) Notes() string     { return "Fake notes paragraph." }
func (f *fakeRunner) Run(_ context.Context, req ShellRequest) (int, error) {
	f.got = req
	if f.write != nil {
		f.write(req)
	}
	return f.exit, f.err
}

func runBash(t *testing.T, tool *BashTool, command string) (string, map[string]any, bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"command": command})
	res, err := tool.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	details, _ := res.Details.(map[string]any)
	return res.Content[0].(provider.TextBlock).Text, details, res.IsError
}

func TestBashToolUsesItsRunner(t *testing.T) {
	f := &fakeRunner{exit: 3, write: func(r ShellRequest) { fmt.Fprint(r.Output, "from the runner\n") }}
	tool := &BashTool{CWD: "/work", Env: map[string]string{"TERVA_HOME": "/th"}, Runner: f}

	text, details, isErr := runBash(t, tool, "echo hi")
	if f.got.Script != "echo hi" || f.got.Dir != "/work" || f.got.Env["TERVA_HOME"] != "/th" {
		t.Fatalf("runner got %+v", f.got)
	}
	if !strings.Contains(text, "from the runner") || !strings.Contains(text, "[exit 3]") || !isErr {
		t.Fatalf("result does not carry the runner's output and status:\n%s", text)
	}
	if details["exit_code"] != 3 {
		t.Fatalf("exit_code = %v", details["exit_code"])
	}
	desc := tool.Description()
	if !strings.Contains(desc, "fake-sh") || !strings.HasSuffix(desc, "\n\nFake notes paragraph.") {
		t.Fatalf("description does not name the runner or carry its notes:\n%s", desc)
	}
}

func TestBashToolRunnerStartError(t *testing.T) {
	tool := &BashTool{CWD: "/work", Runner: &fakeRunner{err: errors.New("no shell")}}
	raw, _ := json.Marshal(map[string]string{"command": "true"})
	if _, err := tool.Execute(context.Background(), raw, nil); err == nil || err.Error() != "start: no shell" {
		t.Fatalf("err = %v, want start: no shell", err)
	}
}

// An in-process runner can write from a background job and the foreground at
// once. The capture must hold every byte, and the race detector must stay quiet.
func TestBashOutputConcurrentWrites(t *testing.T) {
	f := &fakeRunner{write: func(r ShellRequest) {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					fmt.Fprint(r.Output, "q")
				}
			}()
		}
		wg.Wait()
	}}
	text, _, _ := runBash(t, &BashTool{CWD: "/work", Runner: f}, "true")
	if got := strings.Count(text, "q"); got != 800 {
		t.Fatalf("captured %d bytes of 800", got)
	}
}
