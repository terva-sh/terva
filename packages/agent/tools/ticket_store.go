package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// TicketStoreSelection names the store the ticket tools currently work
// against. The zero value means the workspace store, the one
// ticket.Discover finds by walking up from the session cwd.
type TicketStoreSelection struct {
	// Name is the configured name, or empty for the workspace store.
	Name string
	// Path is the configured path, or empty for the workspace store.
	Path string
}

// IsWorkspace reports whether the selection is the discovered workspace store.
func (s TicketStoreSelection) IsWorkspace() bool { return s.Path == "" }

// DisplayName is the name to show for the selection. The workspace store
// answers to config.WorkspaceTicketStoreName, the same name that config
// refuses as a ticket_stores key, so the two cannot drift apart.
func (s TicketStoreSelection) DisplayName() string {
	if s.Name == "" {
		return config.WorkspaceTicketStoreName
	}
	return s.Name
}

// BindCard points the card at this core's store resolver, so the card renders
// the store the tools work against rather than always the workspace one.
//
// It is a separate step because build.go builds the card inside the core's
// struct literal, and open is unexported. Every host that builds a core must
// call it. TestBuiltTicketCardFollowsTheCore holds that for the built
// registry, the same way bindTaskBoard is held.
func (c *TicketCore) BindCard() {
	if c == nil || c.Card == nil {
		return
	}
	c.Card.Open = c.open
	c.Card.StoreName = func() string {
		sel := c.ActiveStore()
		if sel.IsWorkspace() {
			return ""
		}
		return sel.DisplayName()
	}
}

// ActiveStore returns the current selection.
func (c *TicketCore) ActiveStore() TicketStoreSelection {
	c.storeMu.Lock()
	defer c.storeMu.Unlock()
	return c.active
}

// SelectStore points every ticket tool at sel. It opens the store first, so a
// selection that cannot be opened changes nothing and the caller keeps working
// against the store it had.
//
// The card is invalidated on success, because a card rendered from the old
// store would describe tickets the next write will not touch.
func (c *TicketCore) SelectStore(sel TicketStoreSelection) error {
	if sel.IsWorkspace() {
		c.selectWorkspace()
		return nil
	}
	if _, err := openStoreAt(sel.Path); err != nil {
		return err
	}
	c.storeMu.Lock()
	c.active = sel
	c.storeMu.Unlock()
	c.Card.Invalidate()
	return nil
}

// selectWorkspace returns the tools to the discovered store.
func (c *TicketCore) selectWorkspace() {
	c.storeMu.Lock()
	c.active = TicketStoreSelection{}
	c.storeMu.Unlock()
	c.Card.Invalidate()
}

// TicketStoreTool selects the store that the other ticket tools work against.
//
// It classifies like activate_tools: it changes which store the tools reach,
// and it grants no authority of its own. Every store it can select is one that
// user configuration already named, and each write to the selected store still
// faces its own gate.
type TicketStoreTool struct {
	*TicketCore
	// Refused are the ticket_stores entries that failed validation. The tool
	// reports them, because a typo must not disable a store in silence.
	//
	// The stores that survived live on TicketCore.Stores rather than here.
	// The qualified-ref lookup reads the same registry, and a session that
	// registers no ticket_store still holds one, so a second copy on this
	// tool could only drift away from the one the lookup uses.
	Refused []config.TicketStoreRefusal
}

func (t *TicketStoreTool) Name() string          { return "ticket_store" }
func (t *TicketStoreTool) ToolGroupName() string { return "ticket" }

func (t *TicketStoreTool) Description() string {
	return i18n.D("tool.ticket_store.description", "Select the ticket store that the other ticket tools work against. Call this tool with no argument to read the configured stores and the active one. Give store as a configured name to switch to that store. Give store as workspace to return to the store of this repository. The selection holds for the rest of the session. Each store keeps its own tickets, labels, and actors, so a ticket id from one store means nothing in another.")
}

func (t *TicketStoreTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"store":{"type":"string","description":"The name of the store to select. Give workspace for the store of this repository. Omit this field to read the stores without a change."}},"additionalProperties":false}`)
}

// ticketStoreRow is one configured store in the tool's report.
type ticketStoreRow struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Active bool   `json:"active,omitempty"`
}

// ticketStoreRefusalRow is one entry configuration named and validation
// refused, with the reason the user needs to repair it.
type ticketStoreRefusalRow struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type ticketStoreReport struct {
	Active     string                  `json:"active"`
	ActivePath string                  `json:"active_path,omitempty"`
	Stores     []ticketStoreRow        `json:"stores"`
	Refused    []ticketStoreRefusalRow `json:"refused,omitempty"`
}

func (t *TicketStoreTool) report() ticketStoreReport {
	sel := t.ActiveStore()
	rep := ticketStoreReport{Active: sel.DisplayName(), ActivePath: sel.Path}
	// The workspace store always answers, and it carries no configured path.
	rep.Stores = append(rep.Stores, ticketStoreRow{
		Name:   config.WorkspaceTicketStoreName,
		Path:   t.CWD,
		Active: sel.IsWorkspace(),
	})
	for _, s := range t.Stores {
		rep.Stores = append(rep.Stores, ticketStoreRow{
			Name:   s.Name,
			Path:   s.Path,
			Active: !sel.IsWorkspace() && sel.Name == s.Name,
		})
	}
	for _, r := range t.Refused {
		rep.Refused = append(rep.Refused, ticketStoreRefusalRow{Name: r.Name, Reason: r.Reason})
	}
	return rep
}

// names lists what a caller may pass, so a refusal can say it.
func (t *TicketStoreTool) names() []string {
	out := []string{config.WorkspaceTicketStoreName}
	for _, s := range t.Stores {
		out = append(out, s.Name)
	}
	return out
}

func (t *TicketStoreTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in struct {
		Store string `json:"store"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in) // the only argument is optional
	}

	name := strings.TrimSpace(in.Store)
	switch {
	case name == "":
		// A read of the current state, which changes nothing.
		return ticketResult(t.report(), nil)
	case name == config.WorkspaceTicketStoreName:
		t.selectWorkspace()
		return ticketResult(t.report(), nil)
	}

	for _, s := range t.Stores {
		if s.Name != name {
			continue
		}
		if err := t.SelectStore(TicketStoreSelection{Name: s.Name, Path: s.Path}); err != nil {
			return ticketResult(nil, err)
		}
		return ticketResult(t.report(), nil)
	}

	return ticketResult(nil, fmt.Errorf("no ticket store is named %q. The names you can give are: %s",
		name, strings.Join(t.names(), ", ")))
}

// openStoreAt opens the store that a configured path names.
//
// The path can name the directory that holds .tickets, the way ticket.Init
// and ticket.Discover treat a repository root. It can also name the .tickets
// directory itself. Both readings turn up in a hand-written config, so try the
// first and fall back to the second. ticket.Open requires a config file, so a
// directory that is not a store fails here rather than opening empty.
func openStoreAt(path string) (*ticket.Store, error) {
	nested := filepath.Join(path, ticket.StoreDirName)
	if s, err := ticket.Open(nested); err == nil {
		return s, nil
	}
	s, err := ticket.Open(path)
	if err == nil {
		return s, nil
	}
	return nil, fmt.Errorf("no ticket store at %s and none at %s. Run `git ticket init` in the directory you want the store in", nested, path)
}
