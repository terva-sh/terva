package tasktool

import (
	"context"
	"encoding/json"

	"terva.sh/terva/packages/agent/tools/tasks/handlers"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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

func (t listTool) Name() string { return "task_list" }
func (t listTool) Description() string {
	return i18n.D("tool.task_list.description", descList)
}
func (t listTool) Schema() json.RawMessage { return schemaList() }
func (t listTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	text, isErr := handlers.List(t.c.store, raw)
	return result(text, isErr), nil
}

type createTool struct{ c *Controller }

func (t createTool) Name() string { return "task_create" }
func (t createTool) Description() string {
	return i18n.D("tool.task_create.description", descCreate)
}
func (t createTool) Schema() json.RawMessage { return schemaCreate() }
func (t createTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	text, isErr := handlers.Create(t.c.store, raw)
	return result(text, isErr), nil
}

type updateTool struct{ c *Controller }

func (t updateTool) Name() string { return "task_update" }
func (t updateTool) Description() string {
	return i18n.D("tool.task_update.description", descUpdate)
}
func (t updateTool) Schema() json.RawMessage { return schemaUpdate() }
func (t updateTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	text, isErr := handlers.Update(t.c.store, raw)
	return result(text, isErr), nil
}

type archiveTool struct{ c *Controller }

func (t archiveTool) Name() string { return "task_archive" }
func (t archiveTool) Description() string {
	return i18n.D("tool.task_archive.description", descArchive)
}
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

const descList = "Examine the task lists. This tool changes nothing.\n\n" +
	"Usually, call this tool with no arguments. It then gives the current list with the id, " +
	"the status, and the title of each task. Use this to find your position in the work, or " +
	"to see what remains before you finish.\n\n" +
	"Give `archived: true` to get the index of the archived lists. Give `generation: N` to " +
	"read archived list number N. The first archived list is number 1. Do not give " +
	"`generation` when you want the current list.\n\n" +
	"Give `format: \"markdown\"` to get a worklog with checkboxes instead of the compact " +
	"text. This format is most useful with `archived: true`, which gives the full archived " +
	"worklog as one Markdown document, or with `generation: N`."

const descArchive = "Archive the current task list, to start an empty task list for the " +
	"next phase of work.\n\n" +
	"With no arguments, the tool archives all the tasks. This includes the open tasks, which " +
	"have the status pending, active, or blocked. The current list then becomes empty. You " +
	"can still read an archived list with task_list and `archived` or `generation: N`. But " +
	"you cannot continue an archived task. Therefore you must make each unfinished task " +
	"again after the archive operation.\n\n" +
	"Set `keep_open: true` to archive the finished tasks only, which have the status done or " +
	"cancelled. The tool then keeps your open tasks in the current list. Use this to remove " +
	"the finished tasks in the middle of a phase. The optional field `label` gives a name to " +
	"the archived list.\n\n" +
	"Archive at the end of a phase, or before you start a new phase. Give keep_open when the " +
	"open work must continue."

const descCreate = "Make tasks for work that has more than one step. Divide the work, and " +
	"give each step as a separate item of the array in one call. Make one task for each " +
	"step. Do not make one large task such as 'develop' or 'implement everything'.\n\n" +
	"Each task needs a `title` in the imperative form, which names a specific result that " +
	"you can check. The optional fields are `active_form`, `status`, and `note`. The " +
	"default status is 'pending'. The system gives an id to each task, and you must not " +
	"supply your own id. Do not make tasks for a small request of one step."

const descUpdate = "Change a task by `id`. Usually you change the status. An `id` with no " +
	"other field changes nothing, and the tool refuses the call. Therefore always send the " +
	"field that you want to change.\n\n" +
	"Set the status to 'active' before you start a task. One task only can be active. Give " +
	"`evidence` when you set the status to 'done' or 'blocked'. Use 'blocked' and not " +
	"'done' when the work fails or is not complete. You can also change `title`, " +
	"`active_form`, and `note`.\n\n" +
	"When you stop work on a task and you know the next task, give `activate_next` with the " +
	"id of that task. The tool then closes or parks this task and activates the next task " +
	"in one step. This field is valid only with the status 'done', 'cancelled', or " +
	"'blocked'."

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
				"description": "List the archived generations, and do not list the current list.",
			},
			"generation": map[string]any{
				"type":        "integer",
				"description": "Read one archived list by its number. The first list is number 1. Omit this field to get the current list.",
			},
			"format": map[string]any{
				"type":        "string",
				"enum":        []string{"text", "markdown"},
				"description": "The output format. The format \"text\" is the default, and it gives a compact view. The format \"markdown\" gives a worklog with checkboxes. Give \"markdown\" with archived:true for the full archived worklog, or with generation:N for one part of it.",
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
				"description": "The id of the next task to work on. Give this field when you stop work on this task, which is a status of \"done\", \"cancelled\", or \"blocked\". The tool closes or parks this task and activates the next task in the same step. Omit this field when there is no obvious next task.",
			},
		},
		"required": []string{"id"},
	})
	return b
}
