// Package tasktool adapts the pure task store (packages/agent/tools/tasks) to
// the core agent as a built-in capability: the four task tools, the per-turn
// model-facing context card and its open-work signal, the TUI status glance, a
// non-interactive /tasks render, and per-session rebinding. It is the in-core
// replacement for the old terva-tasks extension's app.go glue; the interactive
// panel is intentionally dropped.
package tasktool

import (
	"strings"

	"terva.sh/terva/packages/agent/tools/tasks"
)

// Controller owns one session's task store and exposes the adapters the host
// wires into the agent. It holds no card state of its own: the card, the
// open-work signal, and the status glance are all derived on demand from the
// store each turn, so there is nothing to keep in sync.
type Controller struct {
	store *tasks.Store
}

// New builds a controller over an existing store. The caller wires the store's
// FileStore (via Store().SetFS) and its session id (via Rebind) through the host
// session hook.
func New(store *tasks.Store) *Controller { return &Controller{store: store} }

// Store returns the underlying store so the host can set its FileStore.
func (c *Controller) Store() *tasks.Store { return c.store }

// Rebind re-keys the store to a session (open/resume/fork/new); "" => in-memory.
func (c *Controller) Rebind(id string) error { return c.store.Rebind(id) }

// Policy is the standing task-discipline guidance folded into the system prompt.
func (c *Controller) Policy() string { return contextPolicy }

// cardEscaper neutralizes the few tag-like sequences a model-authored task
// title/note could use to break the card frame or forge a system block. Targeted
// (not a blanket angle-bracket escape) so ordinary < and > in task text survive.
var cardEscaper = strings.NewReplacer(
	"</tasks-context", "<\\/tasks-context",
	"<tasks-context", "<\\tasks-context",
	"<system-reminder", "<\\system-reminder",
	"</system-reminder", "<\\/system-reminder",
)

// Ephemeral renders the live task list as the model-facing context card for the
// per-turn ephemeral tail, framed as a clearly-attributed, non-authoritative
// block. Empty when there are no tasks (the caller omits it). RenderCard is
// already bounded (<= 3.5 KiB), so no further clamp is needed here.
func (c *Controller) Ephemeral() string {
	card := tasks.RenderCard(c.store.List())
	if card == "" {
		return ""
	}
	return "<tasks-context>\n" + cardEscaper.Replace(card) + "\n</tasks-context>"
}

// HasBlocking reports genuine open work (pending or active) so the host's
// end-of-turn gate re-prompts the agent to finish or park it. A blocked task is
// an acknowledged park and does not count (matches tasks.AnyOpen).
func (c *Controller) HasBlocking() bool { return tasks.AnyOpen(c.store.List()) }

// StatusGlance is the short, non-model-facing TUI status-line segment.
func (c *Controller) StatusGlance() string { return tasks.StatusGlance(c.store.List()) }

// Render is the non-interactive /tasks view: the compact current list.
func (c *Controller) Render() string { return tasks.RenderCompact(c.store.List()) }
