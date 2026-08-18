package ctrlproto

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

func imageMsg(data []byte) core.WireMessage {
	return core.WireMessage{
		Role: "user",
		Content: []core.WireBlock{
			{Type: "text", Text: "look"},
			{Type: "image", MimeType: "image/png", Bytes: len(data), Data: data},
		},
	}
}

// FeatureImageData is documented as the contract for OUTBOUND image payloads:
// without it "a serialized carrier strips Data at the connection boundary".
//
// Stripping ran on the two event pumps only, so the rule held for everything
// terva PUSHED and for nothing a client PULLED. conversation.history and
// conversation.reveal both answer with []core.WireMessage — the same transcript
// rows a snapshot carries — so paging up through a session containing
// screenshots shipped every payload to a client that had just declared it could
// not use them.
func TestACommandResultStripsImagesForAClientThatDidNotNegotiate(t *testing.T) {
	payload := []byte{1, 2, 3, 4}

	for _, tc := range []struct {
		name   string
		result any
		msgs   func(any) []core.WireMessage
	}{
		{"conversation.history", HistoryResult{Messages: []core.WireMessage{imageMsg(payload)}},
			func(r any) []core.WireMessage { return r.(HistoryResult).Messages }},
		{"conversation.reveal", RevealResult{Messages: []core.WireMessage{imageMsg(payload)}},
			func(r any) []core.WireMessage { return r.(RevealResult).Messages }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stripResultImageData(tc.result)
			blk := tc.msgs(got)[0].Content[1]
			if len(blk.Data) != 0 {
				t.Errorf("%s shipped %d bytes of image payload to a client that did not negotiate "+
					"image-data", tc.name, len(blk.Data))
			}
			if blk.Bytes != len(payload) {
				t.Errorf("Bytes = %d, want %d — the size must survive so the client can render a placeholder",
					blk.Bytes, len(payload))
			}
			if tc.msgs(got)[0].Content[0].Text != "look" {
				t.Error("stripping removed more than the image payload")
			}

			// Copy-on-strip: the caller's slice is shared and must not be mutated.
			orig := tc.msgs(tc.result)[0].Content[1]
			if len(orig.Data) != len(payload) {
				t.Errorf("the ORIGINAL result was mutated: a shared transcript row lost its payload")
			}
		})
	}
}

// historySvc answers conversation.history with a real image payload, which the
// shared fake does not.
type historySvc struct{ *fakeSvc }

func (h historySvc) History(context.Context, string, int, int, uint64) (HistoryResult, error) {
	return HistoryResult{Total: 1, Messages: []core.WireMessage{imageMsg([]byte{1, 2, 3})}}, nil
}

// The end-to-end half, and the one that matters: stripResultImageData being
// correct proves nothing about respond CALLING it. That is the trap this same
// review already hit once — a helper pinned, its only caller not — so the
// assertion goes through ServeConn, on the bytes a client actually receives.
//
// Both directions in one table: a client that negotiated image-data keeps its
// pixels, a client that did not gets the lean shape. Stripping unconditionally
// would break every image in the web client and must fail here.
func TestConversationHistoryHonoursTheImageDataContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
		wantData bool
	}{
		{"negotiated", []string{FeatureImageData}, true},
		{"lean", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := historySvc{newFakeSvc()}
			client, server := newMemPair()
			go ServeConn(t.Context(), server, svc, ServerHello("terva-test", "0"))

			push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
				Groups: []Group{GroupSession}, Features: tc.features}))
			if sh := pull(t, client); sh.Kind != KindHello {
				t.Fatalf("expected server hello, got %+v", sh)
			}

			push(t, client, mustCmd(t, 1, "s1", MethodConversationHistory, HistoryParams{Before: 10, Limit: 5}))
			r := pull(t, client)
			if r.Kind != KindResp || r.Error != nil {
				t.Fatalf("history resp: %+v", r)
			}

			var got HistoryResult
			if err := json.Unmarshal(r.Result, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Messages) != 1 {
				t.Fatalf("got %d messages", len(got.Messages))
			}
			blk := got.Messages[0].Content[1]
			if has := len(blk.Data) > 0; has != tc.wantData {
				t.Errorf("image payload present = %v, want %v — a pulled result must honour the same "+
					"contract the event pumps do", has, tc.wantData)
			}
			if blk.Bytes != 3 || blk.MimeType != "image/png" {
				t.Errorf("size metadata must survive either way: %+v", blk)
			}
		})
	}
}

// A result that carries no messages must pass through untouched, including the
// types the switch does not name.
func TestAResultWithoutMessagesIsUnchanged(t *testing.T) {
	in := FilesListResult{Files: []FileEntry{{Path: "a.go"}}}
	got := stripResultImageData(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("a message-free result was rewritten: %#v", got)
	}
	if got := stripResultImageData(nil); got != nil {
		t.Errorf("nil result became %#v", got)
	}
}

// The census. A *Result type that carries transcript messages must be handled
// by stripResultImageData — the defect was a result type that carried them and
// was not, and a hand-written switch cannot fail when one is ADDED.
func TestEveryMessageCarryingResultIsStripped(t *testing.T) {
	carriers := resultTypesCarryingMessages(t)
	if len(carriers) < 2 {
		t.Fatalf("found %d message-carrying result types; the scan is not reading the declarations", len(carriers))
	}

	src, err := os.ReadFile("strip.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, name := range carriers {
		if !strings.Contains(body, "case "+name+":") {
			t.Errorf("%s carries []core.WireMessage but stripResultImageData has no arm for it — "+
				"it ships image payloads to clients that did not negotiate image-data", name)
		}
	}
}

// resultTypesCarryingMessages returns every ctrlproto type whose name ends in
// "Result" and which has a field of type []core.WireMessage.
func resultTypesCarryingMessages(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(ts.Name.Name, "Result") {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				if typeIsWireMessageSlice(fld.Type) {
					out = append(out, ts.Name.Name)
					return true
				}
			}
			return true
		})
	}
	return out
}

func typeIsWireMessageSlice(e ast.Expr) bool {
	arr, ok := e.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	sel, ok := arr.Elt.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "core" && sel.Sel.Name == "WireMessage"
}
