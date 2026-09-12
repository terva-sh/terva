package tools

// Qualified ticket references. `ticket:<store>/<id>` names a ticket in another
// store, and the bare `ticket:<id>` names one in this store. The grammar is in
// .tickets/CONVENTIONS.md, and docs/proposals/ticket-agent-workflow.md holds
// the decision under tier 4.
//
// This file resolves the <store> half against the session's store registry and
// stops there. It never opens the other store, and it never reads a ticket out
// of it. That fence belongs to TKT-01M26CWP: a qualified ref is a typed string
// with a lookup, and it is not an edge. A read tool that reached through a ref
// would be an edge under another name. ticket_store stays the only way to
// change which store the tools work against.
//
// The refusal is the point of the lookup. git-ticket compares a reference
// namespace without regard to case, and it compares the identifier exactly. So
// a ref naming a store that nobody registered reports as a reference that
// nobody carries. That failure looks like absence, which is the expensive kind.
// A note that names the store turns it into something the reader can act on.

import (
	"fmt"
	"strings"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/config"
)

// ticketRefNamespace is the namespace half of a ticket reference.
const ticketRefNamespace = "ticket"

// parseQualifiedTicketRef reads ref as `ticket:<store>/<id>`. It returns false
// for a bare `ticket:<id>`, which names a ticket in this store and needs no
// lookup. It also returns false for every other namespace.
//
// git-ticket splits a reference at the first colon, so the whole of
// `<store>/<id>` arrives as the identifier. This function splits it the same
// way. It does not trim the identifier, because the library compares that half
// exactly. A trim here would report a match that no lookup makes.
func parseQualifiedTicketRef(ref string) (store, id string, ok bool) {
	namespace, identifier, found := strings.Cut(ref, ":")
	if !found || !strings.EqualFold(namespace, ticketRefNamespace) {
		return "", "", false
	}
	store, id, found = strings.Cut(identifier, "/")
	if !found || store == "" || id == "" {
		return "", "", false
	}
	return store, id, true
}

// ticketStoreLookup is the answer the registry gives for one qualified ref.
// Path and Note are mutually exclusive. Exactly one of them is set.
type ticketStoreLookup struct {
	// Store is the <store> half of the reference.
	Store string
	// Path is the directory that store lives in. It is set when the lookup
	// resolved.
	Path string
	// Note says why the lookup did not resolve, and it always names the
	// store. It is set when Path is not.
	Note string
}

// resolveTicketStoreRef answers where the store of a qualified ref lives.
func (c *TicketCore) resolveTicketStoreRef(store string) ticketStoreLookup {
	out := ticketStoreLookup{Store: store}
	if store == config.WorkspaceTicketStoreName {
		// The reserved name always means the store of this repository. It
		// does not mean the store that ticket_store selected, because a
		// selection is a property of this session and a ref is committed
		// text that outlives it.
		path, err := c.workspaceStorePath()
		if err != nil {
			out.Note = fmt.Sprintf("%q names the store of this repository. This session cannot open that store. %v", store, err)
			return out
		}
		out.Path = path
		return out
	}
	if !config.ValidTicketStoreName(store) {
		out.Note = fmt.Sprintf("%q cannot be a ticket store name. A store name is lower case, and it matches [a-z0-9-]+. git-ticket compares a reference identifier exactly, so this reference joins no store.", store)
		if lower := strings.ToLower(store); lower != store && config.ValidTicketStoreName(lower) {
			out.Note += fmt.Sprintf(" The author may have meant ticket:%s/...", lower)
		}
		return out
	}
	for _, s := range c.Stores {
		if s.Name == store {
			out.Path = s.Path
			return out
		}
	}
	out.Note = fmt.Sprintf("No ticket store named %q is configured, so this session cannot reach that ticket. %s", store, c.registryAdvice(store))
	return out
}

// registryAdvice names the stores this session does have, and it tells the
// reader where to add one. A refusal that names only the missing store leaves
// the reader to guess whether any registry exists at all.
func (c *TicketCore) registryAdvice(store string) string {
	if len(c.Stores) == 0 {
		return fmt.Sprintf("The user config key ticket_stores names no store. Add %q there to reach this ticket.", store)
	}
	names := make([]string, 0, len(c.Stores))
	for _, s := range c.Stores {
		names = append(names, s.Name)
	}
	return fmt.Sprintf("The user config key ticket_stores names these stores: %s. Add %q there to reach this ticket.", strings.Join(names, ", "), store)
}

// workspaceStorePath is the path of the store this repository carries. The
// result is memoized, because discovery walks the directory tree and one
// ticket can carry several workspace-qualified refs.
func (c *TicketCore) workspaceStorePath() (string, error) {
	c.storeMu.Lock()
	defer c.storeMu.Unlock()
	if !c.wsPathDone {
		s, err := ticket.Discover(c.CWD)
		if err != nil {
			c.wsPathErr = err
		} else {
			c.wsPath = s.Path()
		}
		c.wsPathDone = true
	}
	return c.wsPath, c.wsPathErr
}

// annotateTicketRefs resolves the store of every qualified ref in refs. It
// leaves a bare ref untouched, so a store with no qualified refs pays no
// schema cost at all.
func (c *TicketCore) annotateTicketRefs(refs []ticketReference) []ticketReference {
	if c == nil {
		return refs
	}
	for i, r := range refs {
		store, _, ok := parseQualifiedTicketRef(r.Ref)
		if !ok {
			continue
		}
		got := c.resolveTicketStoreRef(store)
		refs[i].Store = got.Store
		refs[i].StorePath = got.Path
		refs[i].StoreNote = got.Note
	}
	return refs
}
