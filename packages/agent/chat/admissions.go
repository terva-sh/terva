package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Admission modes for an approved non-DM chat.
const (
	// ModeMention: act only on messages that address the bot (a
	// bot_mention entity or an @username in the text). The default —
	// group content is untrusted input, and a bot that answers
	// everything in a busy channel is a nuisance besides.
	ModeMention = "mention"
	// ModeAll: every message in the chat starts a turn.
	ModeAll = "all"
)

// admission is one approved chat's state: its mode plus the optional scope it
// belongs to. Scope is a connector-asserted container id (e.g. a Discord guild)
// that lets RevokeScope drop every chat in a container the bot was removed from;
// it is "" for scopeless surfaces (DMs, or connectors that don't report a
// container). Scope grants nothing on its own — it is only a grouping key.
type admission struct {
	Mode  string `json:"mode"`
	Scope string `json:"scope,omitempty"`
}

// Admissions is the persisted set of non-DM chats the owner has
// approved, with their per-chat mode. The mention gate is UX; THIS is
// the security boundary for group reach — an unapproved chat is
// silent-by-default no matter what it says.
type Admissions struct {
	mu    sync.Mutex
	path  string // "" = in-memory only (tests, bridge without a home)
	chats map[string]admission
}

// AdmissionsPath is the conventional store location for one service.
func AdmissionsPath(tervaHome, service string) string {
	return filepath.Join(tervaHome, "chat", "admissions-"+service+".json")
}

// LoadAdmissions opens the store at path, empty when the file is
// missing or malformed (a broken file must not brick the bot; it just
// forgets approvals, which fails toward silence).
func LoadAdmissions(path string) *Admissions {
	a := &Admissions{path: path, chats: map[string]admission{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return a
	}
	// Current format: {chatID: {"mode":..,"scope":..}}. Fall back to the legacy
	// mode-only map ({chatID: "mention"}) so pre-scope stores load unchanged and
	// upgrade on the next save. The two shapes are cleanly distinguishable — an
	// object value fails to unmarshal into a string and vice versa.
	var m map[string]admission
	if err := json.Unmarshal(data, &m); err == nil && m != nil {
		a.chats = m
		return a
	}
	var legacy map[string]string
	if err := json.Unmarshal(data, &legacy); err == nil {
		for id, mode := range legacy {
			a.chats[id] = admission{Mode: mode}
		}
	}
	return a
}

// Mode returns the chat's admission mode and whether it is approved.
func (a *Admissions) Mode(chatID string) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	adm, ok := a.chats[chatID]
	return adm.Mode, ok
}

// Approve admits a chat (mode ModeMention or ModeAll) with no scope and
// persists. Use ApproveScoped to record the container the chat belongs to.
func (a *Admissions) Approve(chatID, mode string) error {
	return a.ApproveScoped(chatID, mode, "")
}

// ApproveScoped admits a chat and records the scope (container id) it belongs
// to, so RevokeScope can later drop it when the bot leaves that container.
func (a *Admissions) ApproveScoped(chatID, mode, scope string) error {
	if mode != ModeAll {
		mode = ModeMention
	}
	a.mu.Lock()
	a.chats[chatID] = admission{Mode: mode, Scope: scope}
	err := a.save()
	a.mu.Unlock()
	return err
}

// Revoke withdraws a chat's approval and persists. Unknown chats are
// a no-op.
func (a *Admissions) Revoke(chatID string) error {
	a.mu.Lock()
	delete(a.chats, chatID)
	err := a.save()
	a.mu.Unlock()
	return err
}

// RevokeScope withdraws every chat that belongs to scope (a non-empty container
// id) and persists once, returning the number revoked. A "" scope matches
// nothing — scopeless chats are only ever revoked individually, by Revoke. This
// is how a guild kick drops every channel that was approved under that guild,
// not just the one the membership event happened to name.
func (a *Admissions) RevokeScope(scope string) (int, error) {
	if a == nil || scope == "" {
		return 0, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for id, adm := range a.chats {
		if adm.Scope == scope {
			delete(a.chats, id)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, a.save()
}

// save persists under a.mu. In-memory stores (path "") skip disk.
func (a *Admissions) save() error {
	if a.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a.chats, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.path, data, 0o600)
}
