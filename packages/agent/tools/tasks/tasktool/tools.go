package tasktool

import (
	"context"
	"encoding/json"

	"terva.sh/terva/packages/agent/tools/tasks/handlers"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Tools returns the four built-in task tools bound to this controller, ready to
// register in the core tool registry. The handler layer does the arg parsing and
// store mutation and returns (text, isError); the tool wrapper just adapts that
// to a core.ToolResult.
func (c *Controller) Tools() []core.Tool {
	return []core.Tool{listTool{c}, createTool{c}, updateTool{c}, archiveTool{c}}
}

func result(text string, isErr bool) core.ToolResult {
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: text}},
		IsError: isErr,
	}
}

type listTool struct{ c *Controller }

func (t listTool) Name() string            { return "task_list" }
func (t listTool) Description() string     { return descList }
func (t listTool) Schema() json.RawMessage { return schemaList() }
func (t listTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	text, isErr := handlers.List(t.c.store, raw)
	return result(text, isErr), nil
}

type createTool struct{ c *Controller }

func (t createTool) Name() string            { return "task_create" }
func (t createTool) Description() string     { return descCreate }
func (t createTool) Schema() json.RawMessage { return schemaCreate() }
func (t createTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	text, isErr := handlers.Create(t.c.store, raw)
	return result(text, isErr), nil
}

type updateTool struct{ c *Controller }

func (t updateTool) Name() string            { return "task_update" }
func (t updateTool) Description() string     { return descUpdate }
func (t updateTool) Schema() json.RawMessage { return schemaUpdate() }
func (t updateTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	text, isErr := handlers.Update(t.c.store, raw)
	return result(text, isErr), nil
}

type archiveTool struct{ c *Controller }

func (t archiveTool) Name() string            { return "task_archive" }
func (t archiveTool) Description() string     { return descArchive }
func (t archiveTool) Schema() json.RawMessage { return schemaArchive() }
func (t archiveTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	text, isErr := handlers.Archive(t.c.store, raw)
	return result(text, isErr), nil
}

// --- Portable content, copied verbatim from the terva-tasks extension's
// main.go. The standing policy lives in contextPolicy (folded into the system
// prompt); the tool descriptions carry only a terse restatement so the tools
// stay usable if a user opts out of context injection.

const contextPolicy = "You have a task list (task_create / task_update / task_list). " +
	"Its current state is shown to you each turn as a Tasks context card — consult it " +
	"to stay oriented and to decide what remains.\n" +
	"\n" +
	"WHEN: Use tasks for work that is meaningfully multi-step, long-running, risky, or " +
	"interruptible. Skip them for a simple factual answer, a single-file edit, or one " +
	"command.\n" +
	"\n" +
	"PLAN UP FRONT (before you start editing):\n" +
	"- Break the work into its distinct steps and create ONE task per step — in a single " +
	"task_create call, as separate array items. Investigate / implement / test / document " +
	"is four tasks, not one.\n" +
	"- Never create a single umbrella task (\"develop\", \"implement the feature\", \"do " +
	"the work\") and run everything under it. If a title doesn't name a specific, checkable " +
	"outcome, split it.\n" +
	"- Title each task as a specific outcome. GOOD: [\"Add CSV serializer\", \"Wire export " +
	"button to serializer\", \"Add export integration test\", \"Document the export flag\"]. " +
	"BAD: [\"Develop the export feature\"].\n" +
	"\n" +
	"WHILE WORKING:\n" +
	"- Keep exactly one task 'active' at a time — mark a task active before working it.\n" +
	"- Record short evidence when you complete or block a task (a passing test command, " +
	"an edited path, a user clarification).\n" +
	"- Do NOT mark a task 'done' while its tests fail, the work is partial, or errors are " +
	"unresolved — use 'blocked' and say why.\n" +
	"- PHASES: when a distinct phase is finished (or before starting a new one), run " +
	"task_archive to roll the old list off and keep the board focused. With NO arguments it " +
	"archives EVERYTHING and empties the list (that's the default) — pass keep_open:true to " +
	"archive only finished tasks and keep your open ones. Don't archive open work you mean " +
	"to keep unless you set keep_open."

const descList = "Inspect task lists (never mutates). CALL WITH NO ARGUMENTS to get the " +
	"current list (id, status, title) — that is the usual case; use it to reorient or to " +
	"decide what remains before finishing. Only when you want to browse archives: pass " +
	"`archived: true` for the index of archived lists, or `generation: N` to read archived " +
	"list number N (generations are numbered from 1). Do NOT pass `generation` when you " +
	"just want the current list — omit it. Pass `format: \"markdown\"` to render a " +
	"checkbox worklog instead of the compact text — most useful with `archived: true` " +
	"(the whole archived worklog as one Markdown document) or `generation: N`."

const descArchive = "Archive the current task list to start a clean board for the " +
	"next phase of work. DEFAULT (no arguments): archives EVERYTHING — including open " +
	"(pending/active/blocked) tasks — and leaves the current list EMPTY. Archived lists " +
	"stay readable (task_list archived / generation:N) but cannot yet be resumed, so any " +
	"unfinished task you still intend to do must be recreated afterward. Set `keep_open: " +
	"true` to archive only finished (done/cancelled) tasks and KEEP your open tasks in the " +
	"current list — use that to clear completed clutter mid-phase. Optional `label` names " +
	"the archived list. Archive at the END of a phase or BEFORE starting a new one; if open " +
	"work should survive, pass keep_open."

const descCreate = "Create tasks for multi-step work. Decompose the job and pass each " +
	"step as a separate array item in one call — one task per step, not a single " +
	"'develop' / 'implement everything' task. Each needs an imperative `title` naming a " +
	"specific, checkable outcome; optional `active_form` (present-continuous), `status` " +
	"(default 'pending'), and `note`. Ids are system-assigned — never supply your own. " +
	"Don't create tasks for trivial one-step requests."

const descUpdate = "Update a task by `id` — mainly status transitions. Mark a task " +
	"'active' before working it; only one task is active at a time. Provide " +
	"`evidence` when setting 'done' or 'blocked', and use 'blocked' (not 'done') if " +
	"the work is failing or incomplete. May also patch `title`, `active_form`, `note`. " +
	"When you step away from a task (done, cancelled, or blocked) and know which task " +
	"is next, pass `activate_next` with its id — that closes/parks this one and focuses " +
	"the next in a single step (valid only with status 'done', 'cancelled', or 'blocked')."

var statusEnum = []string{"pending", "active", "blocked", "done", "cancelled"}

func schemaList() json.RawMessage {
	// No `minimum` on generation on purpose: a schema-validating host could reject
	// generation:0 before the handler runs, defeating the graceful fall-through
	// that turns a padded generation:0 into "return the current list".
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"archived": map[string]any{
				"type":        "boolean",
				"description": "List the archived generations instead of the current list.",
			},
			"generation": map[string]any{
				"type":        "integer",
				"description": "Read one archived list by its number (numbered from 1). Omit to get the current list.",
			},
			"format": map[string]any{
				"type":        "string",
				"enum":        []string{"text", "markdown"},
				"description": "Output format. \"text\" (default) is the compact inline view. \"markdown\" renders a checkbox worklog — pair with archived:true for the whole archived worklog, or generation:N for one slice.",
			},
		},
	})
	return b
}

func schemaArchive() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"keep_open": map[string]any{"type": "boolean"},
			"label":     map[string]any{"type": "string"},
		},
	})
	return b
}

func schemaCreate() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":       map[string]any{"type": "string"},
						"active_form": map[string]any{"type": "string"},
						"status":      map[string]any{"type": "string", "enum": statusEnum},
						"note":        map[string]any{"type": "string"},
					},
					"required": []string{"title"},
				},
			},
		},
		"required": []string{"tasks"},
	})
	return b
}

func schemaUpdate() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
			"active_form": map[string]any{"type": "string"},
			"status":      map[string]any{"type": "string", "enum": statusEnum},
			"evidence":    map[string]any{"type": "string"},
			"note":        map[string]any{"type": "string"},
			"activate_next": map[string]any{
				"type":        "string",
				"description": "When you step away from this task (status \"done\", \"cancelled\", or \"blocked\"), the id of the next task to focus, activated in the same step. Closes/parks this task and activates that one. Omit if there is no obvious next task.",
			},
		},
		"required": []string{"id"},
	})
	return b
}
