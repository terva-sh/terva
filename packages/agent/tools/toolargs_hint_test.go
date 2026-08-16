package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A model can emit arguments that are valid JSON but semantically wrong: one
// key carrying what should have been several. Observed against a local gemma4
// build, which produced
//
//	{"limit":100000,"offset=859,path":"/x.log"}
//
// where `offset=859,path` is a single key — the model wrote the tool-call DSL's
// `key=value,key=value` separators inside a JSON object. Unmarshalling
// SUCCEEDS: limit binds, the odd key is ignored, and Path stays empty.
//
// The tool then reported a bare "path is required", which is true but useless:
// the model had supplied a path, it was simply attached to a key nobody reads.
// It made the identical mistake three times in a row before the churn detector
// stopped it. Naming the key it did not recognise is what lets it self-correct.
func TestReadUnexpectedKeyIsNamedInError(t *testing.T) {
	tool := &ReadTool{CWD: testsupport.TempDir(t), Sandbox: NewSandbox(testsupport.TempDir(t))}
	raw := json.RawMessage(`{"limit":100000,"offset=859,path":"/x.log"}`)

	_, err := tool.Execute(context.Background(), raw, nil)
	if err == nil {
		t.Fatal("expected an error: path never bound")
	}
	msg := err.Error()
	if !strings.Contains(msg, "path is required") {
		t.Errorf("error lost its original meaning: %q", msg)
	}
	if !strings.Contains(msg, "offset=859,path") {
		t.Errorf("error does not name the unrecognised key, so the model cannot see its mistake: %q", msg)
	}
}

// A plain missing argument is not a malformed call, and its message must not
// grow a confusing tail. This is the common case — the model simply forgot.
func TestReadMissingPathWithNoOddKeysIsUnchanged(t *testing.T) {
	tool := &ReadTool{CWD: testsupport.TempDir(t), Sandbox: NewSandbox(testsupport.TempDir(t))}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"limit":10}`), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got != "path is required" {
		t.Errorf("message = %q, want the bare %q when nothing is unrecognised", got, "path is required")
	}
}

// A well-formed call is untouched: the hint only fires on the failure path.
func TestReadValidCallUnaffectedByHint(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir, Sandbox: NewSandbox(dir)}

	raw := mustJSON(t, map[string]any{"path": path})
	if _, err := tool.Execute(context.Background(), raw, nil); err != nil {
		t.Fatalf("valid call failed: %v", err)
	}
}

// The helper itself: it reports only keys the tool does not know, in a stable
// order, and says nothing at all when every key is recognised.
func TestUnexpectedArgKeys(t *testing.T) {
	known := []string{"path", "offset", "limit"}
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"all recognised", `{"path":"/x","offset":1,"limit":2}`, nil},
		{"empty object", `{}`, nil},
		{"one odd key", `{"limit":1,"offset=859,path":"/x"}`, []string{"offset=859,path"}},
		{"several, sorted", `{"zeta":1,"alpha":2}`, []string{"alpha", "zeta"}},
		{"not an object", `["a","b"]`, nil},
		{"invalid json", `{oops`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unexpectedArgKeys(json.RawMessage(tc.raw), known...)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// Every tool that carries the hint must have a schema the hint can read. The
// grep and glob schemas are assembled by string concatenation, so a stray edit
// can make one unparseable — at which point schemaPropertyNames yields nothing,
// every key looks recognised, and the hint silently stops firing. That failure
// is invisible without this check: the tools still work, they just stop
// explaining themselves.
func TestSchemasYieldPropertyNames(t *testing.T) {
	for _, c := range []struct {
		name   string
		schema string
		want   string // one property that must be present
	}{
		{"read", readSchema, "path"},
		{"write", writeSchema, "content"},
		{"edit", editSchema, "edits"},
		{"bash", bashSchema, "command"},
		{"grep", grepSchema, "pattern"},
		{"glob", globSchema, "pattern"},
	} {
		t.Run(c.name, func(t *testing.T) {
			names := schemaPropertyNames(c.schema)
			if len(names) == 0 {
				t.Fatalf("%s: schema yielded no property names, so the hint can never fire", c.name)
			}
			var found bool
			for _, n := range names {
				if n == c.want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: properties %q do not include %q", c.name, names, c.want)
			}
		})
	}
}

// The hint is a suffix, so a caller can append it to any message. It is empty
// when there is nothing to report, which is what keeps the common case clean.
func TestUnexpectedArgKeyHint(t *testing.T) {
	known := []string{"path", "offset", "limit"}

	if got := unexpectedArgKeyHint(json.RawMessage(`{"path":"/x"}`), known...); got != "" {
		t.Errorf("hint for a clean call = %q, want empty", got)
	}
	got := unexpectedArgKeyHint(json.RawMessage(`{"offset=859,path":"/x"}`), known...)
	if !strings.Contains(got, "offset=859,path") {
		t.Errorf("hint does not name the key: %q", got)
	}
	if !strings.HasPrefix(got, " ") {
		t.Errorf("hint must start with a space so it appends cleanly: %q", got)
	}
}
