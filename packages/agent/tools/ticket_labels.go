package tools

import (
	"context"
	"sort"
	"strings"

	"github.com/terva-sh/git-ticket/ticket"
)

// ticketLabelCap bounds the vocabulary this file derives from use. A store
// with a long tail of one-off labels would otherwise carry all of them in
// every request.
const ticketLabelCap = 20

// ticketLabels is what a store can teach a model about its own labels.
type ticketLabels struct {
	// Enum is the closed set that config.yml declares. The store refuses a
	// label outside this set, so the schema can refuse one too.
	Enum []string
	// InUse holds the labels the tickets already carry, the most common
	// first. A store that declares no vocabulary accepts any label, so these
	// are examples and never a limit.
	InUse []string
}

// labelVocabulary reads the store's labels once and keeps the answer. A full
// scan of this repository's store costs about 56ms, and Schema runs again on
// every tool rebuild, so the cache earns its place.
// labelVocabulary caches per store, not once per session. ticket_store can
// switch stores mid-session, and each store declares its own labels, so a
// single cache would offer the previous store's vocabulary in the write
// schemas. The read runs outside the lock, because it opens the store.
func (c *TicketCore) labelVocabulary() ticketLabels {
	key := c.ActiveStore().Path

	c.storeMu.Lock()
	cached, ok := c.labelsByStore[key]
	c.storeMu.Unlock()
	if ok {
		return cached
	}

	v := c.readLabelVocabulary()

	c.storeMu.Lock()
	if c.labelsByStore == nil {
		c.labelsByStore = map[string]ticketLabels{}
	}
	c.labelsByStore[key] = v
	c.storeMu.Unlock()
	return v
}

// readLabelVocabulary asks the config first, because a declared list is a
// rule the store already enforces. A store that declares none falls back to
// the labels its own tickets carry. No store, or an unreadable one, means an
// empty vocabulary, because Schema runs at registration and must not fail.
func (c *TicketCore) readLabelVocabulary() ticketLabels {
	s, err := c.open()
	if err != nil {
		return ticketLabels{}
	}
	if declared := s.Config().Labels; len(declared) > 0 {
		return ticketLabels{Enum: append([]string(nil), declared...)}
	}
	all, err := s.List(context.Background(), ticket.Filter{All: true})
	if err != nil {
		return ticketLabels{}
	}
	return ticketLabels{InUse: rankLabels(all)}
}

// rankLabels orders the labels by how many tickets carry each one, and breaks
// a tie by name so two builds of the same store give the same schema.
func rankLabels(all []*ticket.Ticket) []string {
	count := map[string]int{}
	for _, t := range all {
		for _, l := range t.Labels {
			if l = strings.TrimSpace(l); l != "" {
				count[l]++
			}
		}
	}
	out := make([]string, 0, len(count))
	for l := range count {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if count[out[i]] != count[out[j]] {
			return count[out[i]] > count[out[j]]
		}
		return out[i] < out[j]
	})
	if len(out) > ticketLabelCap {
		out = out[:ticketLabelCap]
	}
	return out
}

// ticketLabelsProp builds the schema for a field that takes labels. The lead
// sentence names the field, because ticket_create and ticket_update name it
// differently. What follows the lead is the store's own vocabulary, so the
// model reads the values where it types them.
func (c *TicketCore) ticketLabelsProp(lead string) map[string]any {
	v := c.labelVocabulary()
	items := map[string]any{"type": "string"}
	desc := lead
	switch {
	case len(v.Enum) > 0:
		items["enum"] = v.Enum
		desc += " The store accepts these labels only."
	case len(v.InUse) > 0:
		desc += " The store accepts any label. These are the labels it uses now: " +
			strings.Join(v.InUse, ", ") + "."
	default:
		desc += " The store declares no vocabulary, so it accepts any label."
	}
	return map[string]any{"type": "array", "items": items, "description": desc}
}
