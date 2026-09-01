package chat

import "time"

// What an un-admitted chat can leave waiting for the owner's decision.
//
// The gate's not-approved path is silent by design — it is the security
// boundary for group reach — but silence used to mean LOSS: the mention
// that made the owner approve a chat was gone by the time they did, and
// "I approved, so it should answer what I asked" broke every time. So the
// gate HOLDS the last few messages of a chat nobody has admitted, and an
// approval replays the ones that fit the mode the owner chose.
//
// Everything held is untrusted content from a chat the owner has NOT
// approved, which a later approval turns into prompts. The bounds are the
// posture, not tuning: a handful per chat, a handful of chats, minutes not
// hours, text and inline images only (staged files are cleaned up by the
// receive path the moment the gate declines, so a held copy could only
// point at paths that no longer exist), and dropped the moment the answer
// is no.
const (
	heldMax      = 5
	heldMaxAge   = 10 * time.Minute
	heldMaxChats = 32
)

type heldMessage struct {
	at time.Time
	m  Message
}

type heldChat struct {
	last time.Time
	msgs []heldMessage
}

// held is the gate's per-chat buffer. Not safe for concurrent use on its
// own: the gate serializes every call under its mutex.
type held struct {
	now   func() time.Time // tests pin the clock; nil = time.Now
	chats map[string]*heldChat
}

func (h *held) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// add holds m for its chat. Newest wins the per-chat cap, a redelivery
// (same id) replaces its earlier copy, the stalest chat makes room when
// the chat cap is hit, and files never come along.
func (h *held) add(m Message) {
	now := h.clock()
	if h.chats == nil {
		h.chats = map[string]*heldChat{}
	}
	m.Files = nil
	c := h.chats[m.ChatID]
	if c == nil {
		if len(h.chats) >= heldMaxChats {
			h.evictStalest()
		}
		c = &heldChat{}
		h.chats[m.ChatID] = c
	}
	c.prune(now)
	c.last = now
	if m.ID != "" {
		for i := range c.msgs {
			if c.msgs[i].m.ID == m.ID {
				c.msgs[i] = heldMessage{at: now, m: m}
				return
			}
		}
	}
	c.msgs = append(c.msgs, heldMessage{at: now, m: m})
	if len(c.msgs) > heldMax {
		c.msgs = c.msgs[len(c.msgs)-heldMax:]
	}
}

func (h *held) evictStalest() {
	var (
		stalest string
		when    time.Time
		first   = true
	)
	for id, c := range h.chats {
		if first || c.last.Before(when) {
			stalest, when, first = id, c.last, false
		}
	}
	delete(h.chats, stalest)
}

// take removes and returns the chat's unexpired messages, oldest first.
func (h *held) take(chatID string) []Message {
	c := h.chats[chatID]
	if c == nil {
		return nil
	}
	delete(h.chats, chatID)
	c.prune(h.clock())
	out := make([]Message, 0, len(c.msgs))
	for _, hm := range c.msgs {
		out = append(out, hm.m)
	}
	return out
}

// drop forgets a chat outright: the owner said no, or the bot is gone.
func (h *held) drop(chatID string) { delete(h.chats, chatID) }

// edited rewrites a held message in place and deleted withdraws one —
// what keeps a replay honest: the owner approves what the chat says NOW,
// and a message its author took back must not become a turn later.
func (h *held) edited(chatID, id, text string, entities []Entity) bool {
	c := h.chats[chatID]
	if c == nil || id == "" {
		return false
	}
	for i := range c.msgs {
		if c.msgs[i].m.ID == id {
			c.msgs[i].m.Text = text
			c.msgs[i].m.Entities = entities
			return true
		}
	}
	return false
}

func (h *held) deleted(chatID, id string) bool {
	c := h.chats[chatID]
	if c == nil || id == "" {
		return false
	}
	kept := c.msgs[:0]
	found := false
	for _, hm := range c.msgs {
		if hm.m.ID == id {
			found = true
			continue
		}
		kept = append(kept, hm)
	}
	c.msgs = kept
	if len(c.msgs) == 0 {
		delete(h.chats, chatID)
	}
	return found
}

// prune drops what has waited longer than heldMaxAge. An approval an hour
// after the question answers the question, not the conversation of an
// hour ago.
func (c *heldChat) prune(now time.Time) {
	kept := c.msgs[:0]
	for _, hm := range c.msgs {
		if now.Sub(hm.at) <= heldMaxAge {
			kept = append(kept, hm)
		}
	}
	c.msgs = kept
}
