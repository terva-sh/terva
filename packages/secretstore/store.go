// Package secretstore is $TERVA_HOME/secrets.json: the scoped secrets terva
// itself owns, and the grants that say who may reach them.
//
// Phases 1 and 2 encrypted the files terva writes by teaching each writer about
// the codec. That does not scale — every new component needs the same surgery —
// and it cannot reach a component terva does not own at all. The store is the
// answer for the case where terva IS the owner: one file, sealed whole, holding
// values that used to sit in plaintext beside unrelated state.
//
// # One file, not one per scope
//
// The deciding argument is not blast radius — the key opens every scope however
// many files there are — but ROTATION ATOMICITY: one file makes a rotation a
// single atomic re-seal, where a directory can fail halfway and leave scopes
// split across two keys. Second argument: a grant is a RELATION, belonging to
// neither party's file, so splitting forces a grants file alongside with its own
// lock and an ordering problem between the two.
//
// # The store is a source, not the authority
//
// A supervisor may push per-agent secrets down over ctrlproto from a backend
// terva does not own, so Get resolves through an ordered chain rather than
// reading a file. Today the chain is [pushed, file]. A pushed value is
// memory-only by construction — there is no code path that writes one — because
// bolting "do not persist this one" onto a store whose only verb is save is
// where that kind of design goes wrong.
//
// # Grants exist from day one
//
// Even while everything is self-scoped and nothing has been granted. Retrofitting
// authorization into a store that never had it makes every existing caller an
// implicit `allow *`, and there is no later moment when that is easier to fix.
// See docs/proposals/secrets-at-rest.md §8.7 and §8.11.
package secretstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/secrets"
)

// FileName is the store's name under the credential home.
const FileName = "secrets.json"

// formatVersion is bumped only for a change a previous terva could not read.
// Adding a field does not qualify — unknown fields round-trip.
const formatVersion = 1

// Source says where a value came from. It is reported by status surfaces and
// is the reason Value exists rather than Get returning a bare string: "this
// secret is here" and "this secret is here because a supervisor pushed it for
// this run" are different operational facts.
type Source string

const (
	// SourceFile is a value persisted in secrets.json.
	SourceFile Source = "file"
	// SourcePushed is a value delivered at runtime and held in memory only.
	SourcePushed Source = "pushed"
)

// Value is one secret plus its provenance.
type Value struct {
	Value  string
	Source Source
}

// Mode is what a grant permits.
type Mode string

const (
	// ModeUse permits asking the host to ACT with a secret, never to receive
	// it. The hook for the strong delegation form (the credential never leaves
	// the holder), which is why the field exists before anything implements it:
	// adding it later would mean migrating every grant ever written.
	ModeUse Mode = "use"
	// ModeRead permits receiving the opened value, and therefore also permits
	// use — a principal that may hold the material may certainly ask for an
	// action performed with it.
	ModeRead Mode = "read"
)

// permits reports whether holding m satisfies a request for want.
func (m Mode) permits(want Mode) bool {
	if m == ModeRead {
		return want == ModeRead || want == ModeUse
	}
	return m == want
}

// Grant authorizes one principal against one scope. There is no ambient tier:
// a principal reaches its OWN scope and whatever it has been granted, and
// nothing else. Default deny.
type Grant struct {
	Principal string    `json:"principal"`
	Scope     string    `json:"scope"`
	Mode      Mode      `json:"mode"`
	Expires   time.Time `json:"expires,omitempty"` // zero = no expiry
}

// expired reports whether the grant has lapsed as of now.
func (g Grant) expired(now time.Time) bool {
	return !g.Expires.IsZero() && !now.Before(g.Expires)
}

// file is the on-disk shape. Scopes are a FLAT string namespace with a
// convention (core:bot.telegram, ext:memory, conn:matrix, agent:<id>:<name>)
// rather than an enum, so a per-agent pushed secret is just another scope and
// the grant model never grows a dimension.
type file struct {
	Version int                          `json:"version"`
	Scopes  map[string]map[string]string `json:"scopes,omitempty"`
	Grants  []Grant                      `json:"grants,omitempty"`
}

// Store is the handle. Safe for concurrent use, and safe against other
// PROCESSES holding the same home through the same file lock auth.Store uses.
type Store struct {
	path  string
	codec *secrets.Codec

	mu sync.Mutex
	// pushed values, scope -> key -> value. Never written; dropped with the
	// process, which is the point.
	pushed map[string]map[string]string
}

// New returns a store at path. A nil codec reads and writes plaintext; a codec
// whose key resolves to secrets.ErrNoKey does the same until a key exists.
func New(path string, codec *secrets.Codec) *Store {
	return &Store{path: path, codec: codec}
}

func (s *Store) lockPath() string { return s.path + ".lock" }

// ErrNotFound reports that a scope/key holds nothing.
var ErrNotFound = errors.New("secretstore: no such secret")

// Get resolves one secret through the chain: a pushed value first, then the
// file. Pushed wins because it is the live instruction — a supervisor that
// pushes a credential for this run means that one, not whatever was on disk.
func (s *Store) Get(scope, key string) (Value, error) {
	s.mu.Lock()
	if v, ok := s.pushed[scope][key]; ok {
		s.mu.Unlock()
		return Value{Value: v, Source: SourcePushed}, nil
	}
	s.mu.Unlock()

	f, err := s.load()
	if err != nil {
		return Value{}, err
	}
	v, ok := f.Scopes[scope][key]
	if !ok {
		return Value{}, fmt.Errorf("%w: %s/%s", ErrNotFound, scope, key)
	}
	return Value{Value: v, Source: SourceFile}, nil
}

// Set persists one secret. An empty value is a delete, so callers clearing a
// credential do not have to know which verb they want.
func (s *Store) Set(scope, key, value string) error {
	if err := validScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("secretstore: empty key")
	}
	if value == "" {
		return s.Delete(scope, key)
	}
	return s.mutate(func(f *file) {
		if f.Scopes == nil {
			f.Scopes = map[string]map[string]string{}
		}
		if f.Scopes[scope] == nil {
			f.Scopes[scope] = map[string]string{}
		}
		f.Scopes[scope][key] = value
	})
}

// Delete forgets one secret. Missing is not an error — a caller clearing state
// should not have to check first.
func (s *Store) Delete(scope, key string) error {
	return s.mutate(func(f *file) {
		delete(f.Scopes[scope], key)
		if len(f.Scopes[scope]) == 0 {
			delete(f.Scopes, scope)
		}
	})
}

// Push records a runtime-delivered value for this process only. There is no
// path that writes it to disk.
func (s *Store) Push(scope, key, value string) error {
	if err := validScope(scope); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pushed == nil {
		s.pushed = map[string]map[string]string{}
	}
	if s.pushed[scope] == nil {
		s.pushed[scope] = map[string]string{}
	}
	s.pushed[scope][key] = value
	return nil
}

// Keys lists the key NAMES in a scope, pushed and persisted together, sorted.
// Names only: a listing surface that returns values is one leak away from
// being a listing surface that leaks.
func (s *Store) Keys(scope string) ([]string, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for k := range f.Scopes[scope] {
		seen[k] = true
	}
	s.mu.Lock()
	for k := range s.pushed[scope] {
		seen[k] = true
	}
	s.mu.Unlock()
	return slices.Sorted(maps.Keys(seen)), nil
}

// Scopes lists every scope holding at least one secret, sorted.
func (s *Store) Scopes() ([]string, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for sc := range f.Scopes {
		seen[sc] = true
	}
	s.mu.Lock()
	for sc := range s.pushed {
		seen[sc] = true
	}
	s.mu.Unlock()
	return slices.Sorted(maps.Keys(seen)), nil
}

// Grant records an authorization, replacing any existing grant for the same
// principal and scope rather than accumulating duplicates that disagree.
func (s *Store) Grant(g Grant) error {
	if err := validScope(g.Scope); err != nil {
		return err
	}
	if strings.TrimSpace(g.Principal) == "" {
		return errors.New("secretstore: grant with no principal")
	}
	if g.Mode != ModeUse && g.Mode != ModeRead {
		return fmt.Errorf("secretstore: unknown grant mode %q", g.Mode)
	}
	return s.mutate(func(f *file) {
		for i, cur := range f.Grants {
			if cur.Principal == g.Principal && cur.Scope == g.Scope {
				f.Grants[i] = g
				return
			}
		}
		f.Grants = append(f.Grants, g)
	})
}

// Revoke removes a principal's grant on a scope. Missing is not an error.
func (s *Store) Revoke(principal, scope string) error {
	return s.mutate(func(f *file) {
		f.Grants = slices.DeleteFunc(f.Grants, func(g Grant) bool {
			return g.Principal == principal && g.Scope == scope
		})
	})
}

// ForgetResult counts what [Store.Forget] removed and, importantly, what it
// left behind.
type ForgetResult struct {
	// Grants dropped, naming the scope on EITHER side of the arrow.
	Grants int
	// Values deleted — purge only.
	Values int
	// Remaining values left in place. A non-purging caller needs this: "there
	// was nothing here" and "there are three credentials I did not touch" are
	// different answers and must not render the same.
	Remaining int
}

// Forget drops the store's record of a scope: every grant that names it as the
// scope OR as the principal, and — only with purge — the values stored under it.
//
// The two halves are separate because they answer different questions. Dropping
// the grants un-authorizes a component that is gone; deleting its values
// destroys credentials that may still be in use by something merely stopped. A
// user unblocking a rotation (§8.13 Q10) wants the first and would be badly
// surprised by the second, so purge has to be asked for.
//
// Both happen under one lock, so a reader never observes a scope whose grants
// are gone but whose values are not.
func (s *Store) Forget(scope string, purge bool) (ForgetResult, error) {
	if err := validScope(scope); err != nil {
		return ForgetResult{}, err
	}
	var out ForgetResult
	err := s.mutate(func(f *file) {
		before := len(f.Grants)
		f.Grants = slices.DeleteFunc(f.Grants, func(g Grant) bool {
			return g.Scope == scope || g.Principal == scope
		})
		out.Grants = before - len(f.Grants)
		n := len(f.Scopes[scope])
		if purge {
			out.Values = n
			delete(f.Scopes, scope)
			return
		}
		out.Remaining = n
	})
	return out, err
}

// Grants returns every recorded grant, expired ones included — a management
// surface should be able to SHOW an expired grant, which is exactly the
// information a user needs to renew or remove it.
func (s *Store) Grants() ([]Grant, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	return f.Grants, nil
}

// Allows is the authorization decision, and it defaults to DENY.
//
// A principal always reaches its own scope — the scope whose name equals the
// principal's — because an extension storing its own token would otherwise
// need a grant to read back what it just wrote. Everything else requires a
// grant that has not expired and whose mode covers the request.
func (s *Store) Allows(principal, scope string, want Mode) (bool, error) {
	if principal == "" || scope == "" {
		return false, nil
	}
	if principal == scope {
		return true, nil
	}
	f, err := s.load()
	if err != nil {
		return false, err
	}
	now := time.Now()
	for _, g := range f.Grants {
		if g.Principal == principal && g.Scope == scope && !g.expired(now) && g.Mode.permits(want) {
			return true, nil
		}
	}
	return false, nil
}

// Reseal rewrites the whole store, opening with `open` and sealing to
// `sealTo` — the rotation entry point. A store that does not exist yet, or one
// that is still plaintext, is left alone: rotation moves what is already sealed
// onto a new key, and creating a file here would invent state.
func (s *Store) Reseal(open age.Identity, sealTo ...age.Recipient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The same two-step lock every other writer of this file takes, in the same
	// order. Reseal took only s.mu, and s.mu orders NOTHING across processes —
	// nor across a single process, because config.SecretStoreIn mints a fresh
	// Store per call, so two callers hold two different mutexes over one file.
	// A rotation racing an ordinary Set therefore read the pre-Set bytes,
	// re-sealed those, and published them over the Set, silently losing the
	// secret that had just been stored.
	lk, err := filelock.Acquire(s.lockPath())
	if err != nil {
		// A home that cannot host a lockfile must not become a home where
		// rotation fails — same degradation mutate accepts.
		lk = nil
	}
	defer lk.Release()
	// Read INSIDE the lock: bytes read before acquiring it describe the file as
	// it was before the writer we just queued behind.
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !secrets.IsAgeFile(b) {
		return nil
	}
	plain, err := secrets.Decrypt(open, b)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", s.path, err)
	}
	out, err := secrets.Encrypt(plain, sealTo...)
	if err != nil {
		return err
	}
	return privfs.WriteFile(s.path, out)
}

// Encrypted reports whether the file on disk is sealed. For status surfaces:
// it reads the bytes, never the contents.
func (s *Store) Encrypted() (bool, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return secrets.IsAgeFile(b), nil
}

// Path is where the store lives.
func (s *Store) Path() string { return s.path }

// mutate is the read-modify-write, holding the process mutex and then the
// cross-process lock — always that order, which is what keeps two Stores in one
// process from deadlocking against each other.
func (s *Store) mutate(fn func(*file)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lk, err := filelock.Acquire(s.lockPath())
	if err != nil {
		// A home that cannot host a lockfile (read-only mount, exotic
		// filesystem) must not become a home where saving a secret fails.
		// Degrade to the in-process mutex.
		lk = nil
	}
	defer lk.Release()
	// Load INSIDE the lock: anything read before acquiring it describes the
	// file as it was before the writer we just queued behind.
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	fn(&f)
	f.Version = formatVersion
	return s.saveLocked(f)
}

func (s *Store) load() (file, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (file, error) {
	f := file{Version: formatVersion}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	// Content sniff, not a codec-enabled check: the file's own bytes say
	// whether it is encrypted. A plaintext file under an enabled codec still
	// loads (the pre-migration state); an encrypted file is unreadable without
	// the key no matter what the codec thinks, so a nil codec is a hard error
	// naming the file rather than a JSON parse failure on ciphertext.
	if secrets.IsAgeFile(b) {
		if s.codec == nil {
			return f, fmt.Errorf("%s is encrypted but this store has no secrets codec", s.path)
		}
		plain, err := s.codec.Decrypt(b)
		if err != nil {
			return f, fmt.Errorf("decrypt %s: %w", s.path, err)
		}
		b = plain
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return f, nil
}

func (s *Store) saveLocked(f file) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if s.codec != nil {
		on, err := s.codec.Enabled()
		if err != nil {
			// A configured-but-broken key must fail the write rather than
			// silently downgrade it to plaintext.
			return fmt.Errorf("write %s: %w", s.path, err)
		}
		if on {
			if b, err = s.codec.Encrypt(b); err != nil {
				return fmt.Errorf("encrypt %s: %w", s.path, err)
			}
		}
	}
	if err := privfs.MkdirAll(filepath.Dir(s.path)); err != nil {
		return err
	}
	return privfs.WriteFile(s.path, b)
}

// validScope enforces the little the flat namespace requires: a name that can
// be printed in a status table and typed back into a command. Deliberately NOT
// a prefix allowlist — the namespace is open by design (§8.7), and an enum
// here would have to be edited every time a new kind of component appears.
func validScope(scope string) error {
	if strings.TrimSpace(scope) == "" {
		return errors.New("secretstore: empty scope")
	}
	if scope != strings.TrimSpace(scope) || strings.ContainsAny(scope, " \t\r\n") {
		return fmt.Errorf("secretstore: scope %q contains whitespace", scope)
	}
	if !strings.Contains(scope, ":") {
		return fmt.Errorf("secretstore: scope %q needs a principal prefix, e.g. core:%s", scope, scope)
	}
	return nil
}
