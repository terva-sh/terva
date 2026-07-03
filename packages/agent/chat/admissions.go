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

// Admissions is the persisted set of non-DM chats the owner has
// approved, with their per-chat mode. The mention gate is UX; THIS is
// the security boundary for group reach — an unapproved chat is
// silent-by-default no matter what it says.
type Admissions struct {
	mu    sync.Mutex
	path  string // "" = in-memory only (tests, bridge without a home)
	chats map[string]string
}

// AdmissionsPath is the conventional store location for one service.
func AdmissionsPath(tervaHome, service string) string {
	return filepath.Join(tervaHome, "chat", "admissions-"+service+".json")
}

// LoadAdmissions opens the store at path, empty when the file is
// missing or malformed (a broken file must not brick the bot; it just
// forgets approvals, which fails toward silence).
func LoadAdmissions(path string) *Admissions {
	a := &Admissions{path: path, chats: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return a
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return a
	}
	a.chats = m
	return a
}

// Mode returns the chat's admission mode and whether it is approved.
func (a *Admissions) Mode(chatID string) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	mode, ok := a.chats[chatID]
	return mode, ok
}

// Approve admits a chat (mode ModeMention or ModeAll) and persists.
func (a *Admissions) Approve(chatID, mode string) error {
	if mode != ModeAll {
		mode = ModeMention
	}
	a.mu.Lock()
	a.chats[chatID] = mode
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
