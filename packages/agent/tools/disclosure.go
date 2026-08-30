//go:build terva_scripting

package tools

// The disclosure catalog: what a script can discover about the host's tool
// surface. Recorded as proposal §12.7 — a curated subset, not the full
// authority-matched catalog. code_execution discloses a fixed set of
// meta/inspection builtins plus the session's read-only extension and MCP
// tools; code_execution_mutating discloses nothing new, because its binding
// set is already the only true mutating pair.

import (
	"encoding/json"
	"fmt"
	"sort"
)

// CatalogEntry is one disclosed tool: its name, a one-line description, and
// the JSON Schema a script would call it against. Schema is the whole point
// of disclosure over a prose sentence — the script gets the real argument
// shape rather than a hand-written positional adapter.
type CatalogEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	// Source is where the tool comes from: "builtin" for the fixed
	// meta/inspection set, or the extension/MCP provenance for a disclosed
	// plugin tool, so a script (and a reader of its plan) can tell a curated
	// core tool from a session plugin.
	Source string `json:"source"`
}

// DisclosureCatalog is the late-bound answer to "what can this script call
// beyond its fixed bindings?". Built once per session by the wiring layer
// (build.scriptCatalog) where the agent and its ReadOnlySet are in scope,
// and held by the tool so a script can enumerate it. Nil when the tool is
// not wired — the tools()/describe()/call() bindings then fail closed.
//
// The set is a snapshot: a tool installed after wiring does not appear until
// the next session. That is deliberate — the AST pre-check and the approval
// prompt both reason about a catalog that cannot change mid-run.
type DisclosureCatalog struct {
	// entries, keyed by tool name for call()'s membership check.
	entries map[string]CatalogEntry
	// order is the sorted name list for a stable tools() listing.
	order []string
}

// NewDisclosureCatalog builds the snapshot from a set of entries.
func NewDisclosureCatalog(entries []CatalogEntry) *DisclosureCatalog {
	c := &DisclosureCatalog{entries: make(map[string]CatalogEntry, len(entries))}
	for _, e := range entries {
		c.entries[e.Name] = e
		c.order = append(c.order, e.Name)
	}
	sort.Strings(c.order)
	return c
}

// List renders the catalog for tools(): name, description, and source per
// entry, schema omitted so a listing stays cheap.
func (c *DisclosureCatalog) List() string {
	if c == nil {
		return ""
	}
	type row struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}
	rows := make([]row, 0, len(c.order))
	for _, name := range c.order {
		e := c.entries[name]
		rows = append(rows, row{Name: e.Name, Description: e.Description, Source: e.Source})
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	return string(b)
}

// Describe returns the full CatalogEntry (including the schema) for one
// tool, or an error naming it if the tool is not in the catalog.
func (c *DisclosureCatalog) Describe(name string) (CatalogEntry, error) {
	if c == nil {
		return CatalogEntry{}, errNoCatalog()
	}
	e, ok := c.entries[name]
	if !ok {
		return CatalogEntry{}, &notDisclosedError{name: name}
	}
	return e, nil
}

// MayCall reports whether name is disclosed and therefore callable through
// call(name, args).
func (c *DisclosureCatalog) MayCall(name string) bool {
	if c == nil {
		return false
	}
	_, ok := c.entries[name]
	return ok
}

// Names returns the sorted disclosed names, for the AST pre-check's account.
func (c *DisclosureCatalog) Names() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.order...)
}

// notDisclosedError is a call()/describe() refusal for a name outside the
// catalog: not "no such tool", but "not disclosed to this script", which is
// the distinction a reader needs to tell a typo from an authority boundary.
type notDisclosedError struct{ name string }

func (e *notDisclosedError) Error() string {
	return "tool " + e.name + " is not disclosed to this script (it is either absent, not read-only, or outside the disclosed set)"
}

// errNoCatalog is a refusal when the tool was never wired to a catalog.
func errNoCatalog() error {
	return fmt.Errorf("the host-tool catalog is not wired in this session")
}

// curatedMetaBuiltins is the fixed half of the disclosed set (§12.7): the
// meta/inspection builtins that are useful inside a script and carry no
// write authority. Deliberately excluded from the static read-only map:
// activate_tools (visibility only), worktree_list (its four mutating
// siblings are out), and the $TERVA_HOME writers (task_create/update/
// archive, memory, share_file, deliver_result) — read-only in
// classification but not in what a script should reach for.
var curatedMetaBuiltins = []string{
	"session_inspect",
	"session_search",
	"terva_status",
	"task_list",
	"skill",
}

// CuratedMetaBuiltins returns the fixed half of the disclosed set, in order.
func CuratedMetaBuiltins() []string {
	return append([]string(nil), curatedMetaBuiltins...)
}

// IsCuratedMetaBuiltin reports membership in the fixed set.
func IsCuratedMetaBuiltin(name string) bool {
	for _, n := range curatedMetaBuiltins {
		if n == name {
			return true
		}
	}
	return false
}
