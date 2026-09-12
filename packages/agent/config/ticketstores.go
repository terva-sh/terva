package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ticketStoreName is the shape a store name must take, per the ref grammar in
// .tickets/CONVENTIONS.md. A store name is also the <store> of a
// ticket:<store>/<id> reference, and git ticket refs compares the namespace
// without regard to case and the identifier exactly. So ticket:Work/X and
// ticket:work/X are two references that no lookup ever joins, and the miss
// reports as a ref nobody carries rather than as an error.
var ticketStoreName = regexp.MustCompile(`^[a-z0-9-]+$`)

// ValidTicketStoreName reports whether name can be a store name at all. The
// ref resolver asks the same question of the <store> half of a qualified
// reference, which arrives as text from a committed file rather than from
// configuration. Both ask here, so the rule cannot drift into two rules.
func ValidTicketStoreName(name string) bool { return ticketStoreName.MatchString(name) }

// WorkspaceTicketStoreName is the name the workspace store always answers to.
// ticket_stores may not rebind it: the store discovered from the session cwd
// is not configuration's to redirect, and a config entry that shadowed it
// would silently move every write that named it.
const WorkspaceTicketStoreName = "workspace"

// TicketStore is one configured store: the name a caller selects it by, and
// the absolute path it resolves to.
type TicketStore struct {
	Name string
	Path string
}

// TicketStoreRefusal is a ticket_stores entry that did not survive validation,
// with the reason. Refusals are surfaced rather than dropped, because a typo
// in a store path would otherwise disable a store silently.
type TicketStoreRefusal struct {
	Name   string
	Reason string
}

// ResolveTicketStores validates a ticket_stores map and returns the entries
// that survive, sorted by name, plus the ones it refused.
//
// It deliberately does NOT check that a store exists on disk. Nothing in terva
// creates or proposes a ticket store, so a named store that is absent is a
// switch-time error with a message the user can act on, not a config-load
// failure. A store on a detached drive must not break config loading.
//
// tervaHome is excluded as a target: auth.json, sessions/ and the rest of the
// secret deny list live there, and a ticket store nested in it would put an
// agent-writable tree inside the one directory the sandbox denies outright.
func ResolveTicketStores(entries map[string]string, tervaHome string) ([]TicketStore, []TicketStoreRefusal) {
	if len(entries) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	var (
		stores  []TicketStore
		refused []TicketStoreRefusal
	)
	refuse := func(name, reason string) {
		refused = append(refused, TicketStoreRefusal{Name: name, Reason: reason})
	}
	for _, name := range names {
		raw := strings.TrimSpace(entries[name])
		switch {
		case strings.TrimSpace(name) == "":
			refuse(name, "the store name is empty")
			continue
		case name == WorkspaceTicketStoreName:
			refuse(name, fmt.Sprintf("%q names the workspace store, which is always available and cannot be redirected", WorkspaceTicketStoreName))
			continue
		case !ticketStoreName.MatchString(name):
			refuse(name, fmt.Sprintf("%q is not a valid store name. A store name is lower case and matches [a-z0-9-]+, because it is also the <store> of a ticket:<store>/<id> reference", name))
			continue
		case raw == "":
			refuse(name, "the store path is empty")
			continue
		}
		path, err := expandHomePath(raw)
		if err != nil {
			refuse(name, err.Error())
			continue
		}
		if !filepath.IsAbs(path) {
			refuse(name, fmt.Sprintf("%q is relative; a ticket store path must be absolute or start with ~/", raw))
			continue
		}
		path = filepath.Clean(path)
		if tervaHome != "" {
			if home := filepath.Clean(tervaHome); withinRoot(home, path) {
				refuse(name, fmt.Sprintf("%q is inside the terva state directory %q, which the sandbox denies outright", raw, home))
				continue
			}
		}
		stores = append(stores, TicketStore{Name: name, Path: path})
	}
	return stores, refused
}

// expandHomePath resolves a leading ~ against the user's home directory. A ~
// anywhere else is an ordinary character, and ~user is not supported.
func expandHomePath(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand %q: no home directory: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
