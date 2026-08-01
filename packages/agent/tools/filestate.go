package tools

import (
	"crypto/sha256"
	"sync"
	"time"
)

// FileState remembers what the model last SAW of a file — the digest of the
// bytes a read returned or a write/edit produced — so a failed edit can say
// whether the file moved underneath it.
//
// The question it answers is the difference between two instructions the model
// otherwise cannot tell apart. "oldText not found" reads as *you got the code
// wrong*, and the model re-reads and re-derives. But when the file has changed
// since it last looked — a formatter ran, a build step rewrote a generated
// file, the user edited in their editor, a sub-agent landed a commit — the
// model's text was RIGHT and its bytes are merely stale. That is a one-step
// recovery, and only the harness knows which case it is.
//
// Deliberately not a lock or a guard: an edit is never refused because the file
// changed. Terva's edit is exact-match, so a stale oldText already fails on its
// own; this only explains WHY. Refusing on a digest mismatch would also block
// the legitimate case where the model edits a file it correctly predicted the
// contents of.
//
// Bounded and per-tool-set: the map is capped, and the whole thing is dropped
// with the session. Nothing here is durable — a fact about what the model saw
// in THIS session has no meaning in the next one.
type FileState struct {
	mu    sync.Mutex
	seen  map[string]fileSeen
	limit int
}

type fileSeen struct {
	digest [32]byte
	at     time.Time
	how    string // "read" | "write" | "edit"
}

// fileStateLimit caps how many paths are remembered. A session that touches
// more than this is not one where the oldest entry still helps, and an
// unbounded map on a long-running daemon is a leak.
const fileStateLimit = 512

func NewFileState() *FileState {
	return &FileState{seen: map[string]fileSeen{}, limit: fileStateLimit}
}

// Record notes the content the model just saw at path. Called after a
// successful read, write, or edit — the three ways the model learns a file's
// bytes. A nil receiver is a no-op so every call site can stay unconditional.
func (f *FileState) Record(path string, content []byte, how string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Evict when full. Oldest-first rather than LRU: the entries are tiny and
	// the eviction is rare, so the extra bookkeeping to distinguish "last used"
	// from "first recorded" would cost more than it saves.
	if len(f.seen) >= f.limit {
		var oldestPath string
		var oldest time.Time
		for p, s := range f.seen {
			if oldestPath == "" || s.at.Before(oldest) {
				oldestPath, oldest = p, s.at
			}
		}
		delete(f.seen, oldestPath)
	}
	f.seen[path] = fileSeen{digest: sha256.Sum256(content), at: time.Now(), how: how}
}

// Changed reports whether current differs from what the model last saw at path,
// and how long ago it saw it. ok is false when the path was never seen — which
// is NOT the same as unchanged, and callers must not report it as such: a model
// editing a file it never read is a different (and more interesting) case than
// one whose file moved.
func (f *FileState) Changed(path string, current []byte) (changed bool, since time.Duration, how string, ok bool) {
	if f == nil {
		return false, 0, "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, exists := f.seen[path]
	if !exists {
		return false, 0, "", false
	}
	return sha256.Sum256(current) != s.digest, time.Since(s.at), s.how, true
}

// Forget drops a path — used when a file is deleted, so a later file at the
// same path is not compared against bytes that belonged to a different one.
func (f *FileState) Forget(path string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.seen, path)
}
