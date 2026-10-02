package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"terva.sh/terva/packages/privfs"
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
	// modeMuted denies admission until an explicit approval.
	modeMuted = "muted"
)

// admission stores a grant, mute, or inherited restriction. Scope groups
// revocations and grants no access. Parent fields bind a thread to its chat.
type admission struct {
	Mode       string `json:"mode"`
	Scope      string `json:"scope,omitempty"`
	ParentID   string `json:"parent_chat_id,omitempty"`
	ParentKind string `json:"parent_chat_kind,omitempty"`
}

// Group members can create thread IDs without owner commands. Their bindings
// have global and per-parent budgets; owner actions bypass those limits.
const maxParentRecords = 4096

const maxParentRecordsPerChat = 256

// Admissions persists chat grants, parent associations, and thread restrictions.
// Inherited access always depends on the current parent policy.
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
	var versioned struct {
		Version int                  `json:"version"`
		Chats   map[string]admission `json:"chats"`
	}
	if err := json.Unmarshal(data, &versioned); err == nil && versioned.Version != 0 {
		if versioned.Version == 2 && versioned.Chats != nil {
			a.chats = versioned.Chats
		}
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

// Mode returns a standalone chat's explicit grant. Threads with a parent
// association require the gate's sender-aware effective policy instead.
func (a *Admissions) Mode(chatID string) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	adm, ok := a.chats[chatID]
	return adm.Mode, ok && adm.ParentID == "" && adm.ParentKind == "" && (adm.Mode == ModeAll || adm.Mode == ModeMention)
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
	adm := a.chats[chatID]
	adm.Mode = mode
	if scope != "" {
		adm.Scope = scope
	}
	err := a.set(chatID, adm)
	a.mu.Unlock()
	return err
}

// Revoke silences a chat until the owner explicitly approves it again.
// A failed save retains the denial for the running process.
func (a *Admissions) Revoke(chatID string) error {
	a.mu.Lock()
	adm := a.chats[chatID]
	adm.Mode = modeMuted
	a.chats[chatID] = adm
	err := a.save()
	a.mu.Unlock()
	return err
}

// revokeRemoved denies known chats and parents when the bot leaves. An
// unapproved room cannot create persistent records through membership churn.
func (a *Admissions) revokeRemoved(chatID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	adm, known := a.chats[chatID]
	if !known {
		for _, record := range a.chats {
			if record.ParentID == chatID {
				known = true
				break
			}
		}
	}
	if !known {
		return nil
	}
	adm.Mode = modeMuted
	a.chats[chatID] = adm
	return a.save()
}

// RevokeScope revokes chats in a nonempty scope and persists once.
// It preserves child restrictions when it can revoke the group/channel parent.
// Otherwise, it mutes the matched thread without changing the parent.
func (a *Admissions) RevokeScope(scope string) (int, error) {
	_, n, err := a.revokeScope(scope)
	return n, err
}

// revokeScope also returns each denied chat for runtime cleanup, including
// parents whose children omit scope metadata and targets already muted.
func (a *Admissions) revokeScope(scope string) ([]string, int, error) {
	if a == nil || scope == "" {
		return nil, 0, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	targets := map[string]struct{}{}
	for id, adm := range a.chats {
		if adm.Scope != scope {
			continue
		}
		parent := a.chats[adm.ParentID]
		if adm.ParentID != "" && adm.ParentKind != "dm" && (parent.Scope == "" || parent.Scope == scope) {
			targets[adm.ParentID] = struct{}{}
		} else {
			targets[id] = struct{}{}
		}
	}
	n := 0
	denied := make([]string, 0, len(targets))
	for id := range targets {
		denied = append(denied, id)
		adm := a.chats[id]
		if adm.Mode == modeMuted {
			continue
		}
		adm.Mode = modeMuted
		if adm.Scope == "" {
			adm.Scope = scope
		}
		a.chats[id] = adm
		n++
	}
	if n == 0 {
		return denied, 0, nil
	}
	return denied, n, a.save()
}

// set persists a change under a.mu. A failed save retains the old policy.
func (a *Admissions) set(id string, next admission) error {
	old, existed := a.chats[id]
	a.chats[id] = next
	if err := a.save(); err != nil {
		if existed {
			a.chats[id] = old
		} else {
			delete(a.chats, id)
		}
		return err
	}
	return nil
}

// bind remembers the parent without granting access. Metadata cannot later
// disappear or change the parent to escape a restriction.
// It reports valid metadata separately from a persistence failure.
func (a *Admissions) bind(m Message, owner string) (bool, error) {
	if a == nil {
		return false, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	adm, exists := a.chats[m.ChatID]
	if adm.ParentID != "" || adm.ParentKind != "" {
		if adm.ParentID != m.ParentChatID || adm.ParentKind != m.ParentChatKind {
			return false, nil
		}
		if m.ScopeID != "" && adm.Scope != m.ScopeID {
			adm.Scope = m.ScopeID
			return true, a.set(m.ChatID, adm)
		}
		return true, nil
	}
	if parent := a.chats[m.ParentChatID]; parent.ParentID != "" || parent.ParentKind != "" {
		return false, nil
	}
	parent := a.chats[m.ParentChatID]
	if !exists && m.UserID != owner && parent.Mode != ModeAll && parent.Mode != ModeMention {
		// Held messages retain their context until approval. Unapproved
		// senders cannot fill the persistent association budget.
		return true, nil
	}
	if !exists && m.UserID != owner {
		parents, siblings := 0, 0
		for _, record := range a.chats {
			if record.ParentID != "" || record.ParentKind != "" {
				parents++
			}
			if record.ParentID == m.ParentChatID {
				siblings++
			}
		}
		if parents >= maxParentRecords || siblings >= maxParentRecordsPerChat {
			return false, nil
		}
	}
	adm.ParentID, adm.ParentKind = m.ParentChatID, m.ParentChatKind
	if m.ScopeID != "" {
		adm.Scope = m.ScopeID
	}
	return true, a.set(m.ChatID, adm)
}

// effective intersects the thread restriction with the current parent policy.
func (a *Admissions) effective(m Message, owner string) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	adm := a.chats[m.ChatID]
	if adm.Mode == modeMuted {
		return "", false
	}
	if adm.ParentID == "" && adm.ParentKind == "" {
		return adm.Mode, adm.Mode == ModeAll || adm.Mode == ModeMention
	}
	if m.ChatKind != "thread" || m.ParentChatID != adm.ParentID || m.ParentChatKind != adm.ParentKind {
		return "", false
	}
	parent := a.chats[adm.ParentID]
	if parent.Mode == modeMuted || parent.ParentID != "" || parent.ParentKind != "" {
		return "", false
	}
	mode := parent.Mode
	if adm.ParentKind == "dm" {
		if owner == "" || m.UserID != owner {
			return "", false
		}
		mode = ModeAll
	} else if mode != ModeAll && mode != ModeMention {
		return "", false
	}
	if adm.Mode == ModeMention {
		mode = ModeMention
	} else if adm.Mode != "" && adm.Mode != ModeAll {
		return "", false
	}
	return mode, true
}

func (a *Admissions) inherited(chatID string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	adm := a.chats[chatID]
	return adm.ParentID != "" || adm.ParentKind != ""
}

// save persists under a.mu. In-memory stores (path "") skip disk.
func (a *Admissions) save() error {
	if a.path == "" {
		return nil
	}
	if err := privfs.MkdirAll(filepath.Dir(a.path)); err != nil {
		return err
	}
	// Older hosts treat any map entry as a grant, including a mute or an
	// association without a mode. A numeric version makes both legacy map
	// decoders reject this file and fail toward silence on downgrade.
	data, err := json.MarshalIndent(struct {
		Version int                  `json:"version"`
		Chats   map[string]admission `json:"chats"`
	}{Version: 2, Chats: a.chats}, "", "  ")
	if err != nil {
		return err
	}
	// Atomically: the admissions list is an access-control record, and a reader
	// catching it mid-truncate would see an empty allow-list.
	return privfs.WriteFile(a.path, data)
}
