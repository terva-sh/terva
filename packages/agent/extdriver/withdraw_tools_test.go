package extdriver

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

// withdrawRecorder records Notify (to sequence the test over the in-order
// read loop) and RefreshTools (the hook under test). Everything else is the
// embedded no-op.
type withdrawRecorder struct {
	stubHooks
	mu        sync.Mutex
	notifies  []string
	refreshes int
}

func (r *withdrawRecorder) Notify(_, _, msg string) {
	r.mu.Lock()
	r.notifies = append(r.notifies, msg)
	r.mu.Unlock()
}

func (r *withdrawRecorder) RefreshTools() {
	r.mu.Lock()
	r.refreshes++
	r.mu.Unlock()
}

func (r *withdrawRecorder) sawNotify(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.notifies, sub)
}

func (r *withdrawRecorder) refreshCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshes
}

// extWithTools builds a bare Driver holding one extension with the named
// tools registered, for unit tests that don't need a live subprocess.
func extWithTools(name string, tools ...string) (*Driver, *Extension) {
	ext := &Extension{Manifest: Manifest{Name: name}}
	idx := map[string]*Extension{}
	for _, t := range tools {
		ext.tools = append(ext.tools, extproto.RegisterToolFromExt{Name: t})
		idx[t] = ext
	}
	d := &Driver{ext: map[string]*Extension{name: ext}, toolIndex: idx}
	return d, ext
}

func TestResolveWithdrawn(t *testing.T) {
	_, ext := extWithTools("git", "gitlog", "gitstatus")

	all := resolveWithdrawn(ext, extproto.SetWithdrawnToolsFromExt{All: true})
	if !sameSet(all, map[string]bool{"gitlog": true, "gitstatus": true}) {
		t.Errorf("All:true expanded to %v; want both registered tools", all)
	}

	// Explicit names intersect with the ext's own tools: a name the
	// extension never registered (or a built-in) is dropped.
	some := resolveWithdrawn(ext, extproto.SetWithdrawnToolsFromExt{Tools: []string{"gitlog", "read", "bogus"}})
	if !sameSet(some, map[string]bool{"gitlog": true}) {
		t.Errorf("explicit names resolved to %v; want only the owned gitlog", some)
	}

	// Empty request is the restore-all zero value.
	if got := resolveWithdrawn(ext, extproto.SetWithdrawnToolsFromExt{Tools: []string{}}); got != nil {
		t.Errorf("empty request resolved to %v; want nil", got)
	}
	// Explicit list of only-foreign names also collapses to nil.
	if got := resolveWithdrawn(ext, extproto.SetWithdrawnToolsFromExt{Tools: []string{"read"}}); got != nil {
		t.Errorf("all-foreign request resolved to %v; want nil", got)
	}
}

func TestSameSet(t *testing.T) {
	cases := []struct {
		a, b map[string]bool
		want bool
	}{
		{nil, nil, true},
		{nil, map[string]bool{}, true}, // nil == empty
		{map[string]bool{"x": true}, map[string]bool{"x": true}, true},
		{map[string]bool{"x": true}, nil, false},
		{map[string]bool{"x": true}, map[string]bool{"y": true}, false},
		{map[string]bool{"x": true}, map[string]bool{"x": true, "y": true}, false},
	}
	for i, c := range cases {
		if got := sameSet(c.a, c.b); got != c.want {
			t.Errorf("case %d: sameSet(%v,%v)=%v; want %v", i, c.a, c.b, got, c.want)
		}
	}
}

// TestDriverToolsFlagsWithdrawn proves the single enumeration seam: a
// withdrawn tool is FLAGGED (not dropped) by Driver.Tools, and stays
// indexed (HasTool true) so a later restore needs no re-registration.
func TestDriverToolsFlagsWithdrawn(t *testing.T) {
	d, ext := extWithTools("git", "gitlog", "gitstatus")
	ext.withdrawnTools = map[string]bool{"gitlog": true}

	got := map[string]bool{}
	for _, ti := range d.Tools() {
		got[ti.Name] = ti.Withdrawn
	}
	if len(got) != 2 {
		t.Fatalf("Tools() returned %d tools; want both still present (flagged, not dropped)", len(got))
	}
	if !got["gitlog"] {
		t.Error("gitlog should be flagged Withdrawn")
	}
	if got["gitstatus"] {
		t.Error("gitstatus should not be flagged Withdrawn")
	}
	if !d.HasTool("gitlog") {
		t.Error("a withdrawn tool must stay indexed (HasTool) so restore needs no re-registration")
	}
}

// TestSetWithdrawnToolsFiresHookOnceThenNoOps drives the full wire path: an
// extension registers a tool, withdraws all (fires RefreshTools once), then
// re-asserts the same set (must be a no-op — cache safety §4). The driver's
// view must show the tool flagged Withdrawn.
func TestSetWithdrawnToolsFiresHookOnceThenNoOps(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "git")
	hello := `{"type":"hello","name":"git","version":"1.0","capabilities":["tools","events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"register_tool","name":"gitlog","schema":{"type":"object"}}'
printf '%s\n' '{"type":"set_withdrawn_tools","all":true}'
printf '%s\n' '{"type":"notify","level":"info","message":"ONE"}'
printf '%s\n' '{"type":"set_withdrawn_tools","all":true}'
printf '%s\n' '{"type":"notify","level":"info","message":"TWO"}'
while IFS= read -r line; do case "$line" in *'"type":"shutdown"'*) exit 0;; esac; done
`
	writeShellExt(t, dir, hello, body)

	hooks := &withdrawRecorder{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	if err := d.Load(context.Background(), dir, Manifest{Name: "git", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	// TWO is emitted after the SECOND set_withdrawn_tools, so seeing it
	// means both frames were processed by the in-order read loop.
	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("TWO") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("TWO") {
		t.Fatal("extension never reached TWO; set_withdrawn_tools may not have been processed")
	}

	if n := hooks.refreshCount(); n != 1 {
		t.Errorf("RefreshTools fired %d times; want 1 (the redundant re-assert must no-op)", n)
	}

	var flagged, found bool
	for _, ti := range d.Tools() {
		if ti.Name == "gitlog" {
			found, flagged = true, ti.Withdrawn
		}
	}
	if !found {
		t.Fatal("gitlog missing from Driver.Tools after withdrawal (should be flagged, not dropped)")
	}
	if !flagged {
		t.Error("gitlog should be flagged Withdrawn after set_withdrawn_tools all:true")
	}
}
