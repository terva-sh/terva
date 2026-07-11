package extdriver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// TestDiagnosticsReportsRegistrationsAndReady loads an extension that
// registers a command and a tool then signals ready, and asserts the
// Diagnostics snapshot reflects all of it as active/ready.
func TestDiagnosticsReportsRegistrationsAndReady(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "diagext")
	hello := `{"type":"hello","name":"diagext","version":"1.2","capabilities":["commands","tools"]}`
	body := `printf '%s\n' '{"type":"register_command","name":"greet","description":"say hi"}'
printf '%s\n' '{"type":"register_tool","name":"lookup","description":"look things up","schema":{"type":"object"}}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	writeShellExt(t, dir, hello, body)

	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", stubHooks{})
	if err := d.Load(context.Background(), dir, Manifest{Name: "diagext", Version: "1.2", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)
	d.WaitForReady(testsupport.ExtReadyGrace)

	diags := d.Diagnostics()
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %d entries, want 1", len(diags))
	}
	dg := diags[0]
	if dg.Name != "diagext" || dg.Version != "1.2" {
		t.Fatalf("name/version = %q/%q", dg.Name, dg.Version)
	}
	if !dg.Ready || dg.ReadyTimedOut || dg.AutoReady {
		t.Fatalf("ready flags = ready:%v timeout:%v auto:%v (want ready only)", dg.Ready, dg.ReadyTimedOut, dg.AutoReady)
	}
	if len(dg.Commands) != 1 || dg.Commands[0].Name != "greet" || !dg.Commands[0].Active {
		t.Fatalf("commands = %+v", dg.Commands)
	}
	if len(dg.Tools) != 1 || dg.Tools[0].Name != "lookup" || !dg.Tools[0].Active {
		t.Fatalf("tools = %+v", dg.Tools)
	}
	if len(dg.Messages) != 0 {
		t.Fatalf("unexpected diagnostic messages: %v", dg.Messages)
	}
}

// TestDiagnosticsRecordsToolNameConflict loads two extensions that register
// the same tool name. The first owns the active slot; the second is recorded
// as inactive with a conflict message. Loading sequentially makes the winner
// deterministic (the first's registration lands before the second loads).
func TestDiagnosticsRecordsToolNameConflict(t *testing.T) {
	tmp := testsupport.TempDir(t)
	mkExt := func(name string) string {
		dir := filepath.Join(tmp, name)
		hello := `{"type":"hello","name":"` + name + `","version":"1.0","capabilities":["tools"]}`
		body := `printf '%s\n' '{"type":"register_tool","name":"dup","description":"d","schema":{"type":"object"}}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
		writeShellExt(t, dir, hello, body)
		return dir
	}
	dirA := mkExt("alpha")
	dirB := mkExt("beta")

	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", stubHooks{})
	defer d.Stop(2 * time.Second)

	if err := d.Load(context.Background(), dirA, Manifest{Name: "alpha", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load alpha: %v", err)
	}
	d.WaitForReady(testsupport.ExtReadyGrace) // alpha's dup registration lands first
	if err := d.Load(context.Background(), dirB, Manifest{Name: "beta", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load beta: %v", err)
	}
	d.WaitForReady(testsupport.ExtReadyGrace)

	byName := map[string]ExtensionDiagnostic{}
	for _, dg := range d.Diagnostics() {
		byName[dg.Name] = dg
	}
	if len(byName["alpha"].Tools) != 1 || !byName["alpha"].Tools[0].Active {
		t.Fatalf("alpha tools = %+v, want dup active", byName["alpha"].Tools)
	}
	beta := byName["beta"]
	if len(beta.Tools) != 1 || beta.Tools[0].Active {
		t.Fatalf("beta tools = %+v, want dup inactive (shadowed)", beta.Tools)
	}
	sawConflict := false
	for _, m := range beta.Messages {
		if m == "tool dup conflicts with another extension registering the same name" {
			sawConflict = true
		}
	}
	if !sawConflict {
		t.Fatalf("beta diagnostics missing tool-conflict message: %v", beta.Messages)
	}
}
