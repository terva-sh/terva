package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// execTaskTool runs one task tool from reg and returns its text, failing the
// test on a dispatch or tool error.
func execTaskTool(t *testing.T, reg core.Registry, name, args string) string {
	t.Helper()
	tool, err := reg.Get(name)
	if err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	if res.IsError {
		t.Fatalf("%s errored: %s", name, b.String())
	}
	return b.String()
}

// sharedTasksResolved builds the slice of Resolved that freshTasksRegistry
// consumes: a registry whose task tools close over one shared controller,
// exactly as Resolve wires it.
func sharedTasksResolved() (Resolved, *tasktool.Controller) {
	shared := tasktool.New(tasks.NewStore(nil, "agent"))
	reg := core.Registry{}
	for _, tl := range shared.Tools() {
		reg[tl.Name()] = tl
	}
	return Resolved{ToolRegistry: reg, Tasks: shared}, shared
}

// TestFreshTasksRegistryIsolatesBoards pins the R5 Batch-A regression: a
// per-conversation registry's task tools must mutate ONLY their own board.
// Before the fix, bot group agents shared resolved.Tasks, so an admitted
// group could read and mutate the owner DM's persisted board.
func TestFreshTasksRegistryIsolatesBoards(t *testing.T) {
	r, shared := sharedTasksResolved()

	regA, ctrlA := r.freshTasksRegistry()
	regB, ctrlB := r.freshTasksRegistry()
	if ctrlA == nil || ctrlB == nil {
		t.Fatal("fresh controllers should exist when r.Tasks is set")
	}
	if ctrlA == shared || ctrlB == shared || ctrlA == ctrlB {
		t.Fatal("each fresh registry must get its own controller")
	}

	execTaskTool(t, r.ToolRegistry, "task_create", `{"tasks":[{"title":"owner secret task"}]}`)
	execTaskTool(t, regA, "task_create", `{"tasks":[{"title":"group A task"}]}`)
	execTaskTool(t, regB, "task_create", `{"tasks":[{"title":"group B task"}]}`)

	boards := map[string]string{
		"owner":   execTaskTool(t, r.ToolRegistry, "task_list", `{}`),
		"group A": execTaskTool(t, regA, "task_list", `{}`),
		"group B": execTaskTool(t, regB, "task_list", `{}`),
	}
	for who, board := range boards {
		for other, want := range map[string]string{
			"owner": "owner secret task", "group A": "group A task", "group B": "group B task",
		} {
			has := strings.Contains(board, want)
			if who == other && !has {
				t.Errorf("%s board lost its own task: %q", who, board)
			}
			if who != other && has {
				t.Errorf("%s board leaked %s's task: %q", who, other, board)
			}
		}
	}

	// The context cards (what each conversation's model sees) are equally isolated.
	if card := ctrlA.Ephemeral(); strings.Contains(card, "owner secret task") {
		t.Errorf("group A context card leaked the owner board: %q", card)
	}
	if card := shared.Ephemeral(); strings.Contains(card, "group A task") {
		t.Errorf("owner context card leaked a group board: %q", card)
	}

	// The open-work gate reads per-conversation too: completing A's task must
	// not depend on owner/B state.
	if !ctrlA.HasBlocking() {
		t.Fatal("group A should report open work")
	}
}

// TestFreshTasksRegistrySharesNonTaskTools pins that the clone is shallow
// everywhere else: non-task tools stay the same instances, so late-bound
// fallbacks and gates behave exactly as with the shared registry.
func TestFreshTasksRegistrySharesNonTaskTools(t *testing.T) {
	r, _ := sharedTasksResolved()
	marker := fakeStaticTool{name: "marker"}
	r.ToolRegistry["marker"] = marker

	reg, _ := r.freshTasksRegistry()
	if got, _ := reg.Get("marker"); got != marker {
		t.Error("non-task tools must be shared instances, not copies")
	}
	if shared, _ := r.ToolRegistry.Get("task_create"); shared == mustGet(t, reg, "task_create") {
		t.Error("task tools must be rebound, not shared")
	}
}

// TestFreshTasksRegistryRespectsToolsAllowlist pins that a --tools allowlist
// that dropped a task tool keeps it dropped in the per-conversation clone.
func TestFreshTasksRegistryRespectsToolsAllowlist(t *testing.T) {
	r, _ := sharedTasksResolved()
	delete(r.ToolRegistry, "task_archive")

	reg, _ := r.freshTasksRegistry()
	if _, err := reg.Get("task_archive"); err == nil {
		t.Error("a task tool the resolve dropped must stay dropped in the clone")
	}
}

// TestFreshTasksRegistryNilWithoutTasks pins the chat/play/--no-tools path:
// no controller, no clone.
func TestFreshTasksRegistryNilWithoutTasks(t *testing.T) {
	r := Resolved{ToolRegistry: core.Registry{}}
	if reg, ctrl := r.freshTasksRegistry(); reg != nil || ctrl != nil {
		t.Errorf("want (nil, nil) without r.Tasks, got (%v, %v)", reg, ctrl)
	}
}

func mustGet(t *testing.T, reg core.Registry, name string) core.Tool {
	t.Helper()
	tool, err := reg.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// fakeStaticTool is a minimal comparable tool for identity assertions.
type fakeStaticTool struct{ name string }

func (f fakeStaticTool) Name() string            { return f.name }
func (f fakeStaticTool) Description() string     { return f.name }
func (f fakeStaticTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fakeStaticTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}
