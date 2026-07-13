package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestBotChatAgentsGetFreshTaskBoards guards the R5 Batch-A isolation fix at
// the wiring site: the NewChatAgent closure in botcmd.go must mint group
// agents via NewAgentWithFreshTasks (per-conversation in-memory board), never
// via plain NewAgent over the shared registry — whose task tools close over
// resolved.Tasks, the controller bound to the owner DM's persisted session.
// Parsed from source so the guard holds under every build tag.
func TestBotChatAgentsGetFreshTaskBoards(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "botcmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var closure *ast.FuncLit
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "NewChatAgent" {
			if fn, ok := kv.Value.(*ast.FuncLit); ok {
				closure = fn
			}
		}
		return true
	})
	if closure == nil {
		t.Fatal("botcmd.go no longer assigns a NewChatAgent closure — update this guard alongside the new wiring")
	}

	fresh, shared := false, false
	ast.Inspect(closure, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "NewAgentWithFreshTasks":
				fresh = true
			case "NewAgent":
				shared = true
			}
		}
		return true
	})
	if !fresh {
		t.Error("NewChatAgent must mint group agents with NewAgentWithFreshTasks (isolated task board)")
	}
	if shared {
		t.Error("NewChatAgent must not call NewAgent — the shared registry's task tools mutate the owner DM's board")
	}
}
