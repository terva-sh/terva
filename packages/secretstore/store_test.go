package secretstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

// sealedStore returns a store whose codec holds a live key, plus the identity,
// the way a home looks after `terva secret init`.
func sealedStore(t *testing.T) (*Store, *age.X25519Identity) {
	t.Helper()
	dir := testsupport.TempDir(t)
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	codec := secrets.NewCodec(func() (*age.X25519Identity, error) { return id, nil })
	return New(filepath.Join(dir, FileName), codec), id
}

// plainStore returns a store on a home with no key at all — the pre-init
// state, where everything must still work in the clear.
func plainStore(t *testing.T) *Store {
	t.Helper()
	dir := testsupport.TempDir(t)
	codec := secrets.NewCodec(func() (*age.X25519Identity, error) { return nil, secrets.ErrNoKey })
	return New(filepath.Join(dir, FileName), codec)
}

func TestSetGetDelete(t *testing.T) {
	s, _ := sealedStore(t)
	if err := s.Set("core:bot.telegram", "token", "tg-secret"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("core:bot.telegram", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "tg-secret" || got.Source != SourceFile {
		t.Fatalf("got %+v", got)
	}
	if err := s.Delete("core:bot.telegram", "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("core:bot.telegram", "token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	// Deleting again is fine — a caller clearing state should not have to check.
	if err := s.Delete("core:bot.telegram", "token"); err != nil {
		t.Fatal(err)
	}
}

// The payoff, and the whole reason the store exists: a token that used to sit
// in plaintext beside unrelated state is now unreadable on disk.
func TestValuesAreSealedOnDisk(t *testing.T) {
	s, _ := sealedStore(t)
	if err := s.Set("core:bot.discord", "token", "dc-secret-value"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsAgeFile(raw) {
		t.Fatal("store is not encrypted on a home that has a key")
	}
	if strings.Contains(string(raw), "dc-secret-value") {
		t.Fatal("the secret is readable on disk")
	}
	// The scope name is hidden too — whole-file sealing costs nothing extra to
	// hide the inventory, and knowing WHICH services you use is worth hiding.
	if strings.Contains(string(raw), "core:bot.discord") {
		t.Error("the scope inventory is readable on disk")
	}
}

// With no key configured the store must behave exactly as before — the feature
// is inert until someone opts in, and a fresh home that has not run
// `secret init` still has to be able to save a bot token.
func TestNoKeyWritesPlaintext(t *testing.T) {
	s := plainStore(t)
	if err := s.Set("core:bot.telegram", "token", "tg-plain"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if secrets.IsAgeFile(raw) {
		t.Fatal("wrote ciphertext with no key configured")
	}
	got, err := s.Get("core:bot.telegram", "token")
	if err != nil || got.Value != "tg-plain" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// A configured-but-BROKEN key must fail the write rather than silently
// downgrade it to plaintext. This is the ErrNoKey/other-error distinction that
// makes "encryption is off" different from "encryption is broken".
func TestBrokenKeyRefusesToWritePlaintext(t *testing.T) {
	dir := testsupport.TempDir(t)
	boom := errors.New("key file is unreadable")
	codec := secrets.NewCodec(func() (*age.X25519Identity, error) { return nil, boom })
	s := New(filepath.Join(dir, FileName), codec)

	err := s.Set("core:bot.telegram", "token", "tg-secret")
	if err == nil {
		t.Fatal("wrote with a broken key configured")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error does not name the cause: %v", err)
	}
	if _, statErr := os.Stat(s.Path()); !os.IsNotExist(statErr) {
		t.Error("a refused write still created the file")
	}
}

// A pushed value is memory-only and beats the file. Both halves matter: it must
// not be written (that is the supervisor's business, not ours to persist), and
// it must win, because a supervisor pushing a credential for this run means
// that one rather than whatever was on disk.
func TestPushedValueIsMemoryOnlyAndWins(t *testing.T) {
	s, id := sealedStore(t)
	if err := s.Set("agent:builder-3:api", "key", "from-disk"); err != nil {
		t.Fatal(err)
	}
	if err := s.Push("agent:builder-3:api", "key", "from-supervisor"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("agent:builder-3:api", "key")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "from-supervisor" {
		t.Errorf("pushed value did not win: %+v", got)
	}
	if got.Source != SourcePushed {
		t.Errorf("source = %q, want %q — status surfaces report provenance", got.Source, SourcePushed)
	}

	// Nothing on disk changed.
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := secrets.Decrypt(id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "from-supervisor") {
		t.Fatal("a pushed value was persisted")
	}
	if !strings.Contains(string(plain), "from-disk") {
		t.Fatal("pushing overwrote the persisted value")
	}
}

// Keys and Scopes list NAMES, never values, and cover pushed and persisted
// alike — a caller enumerating what it holds should not see a different world
// depending on where a value came from.
func TestListingCoversBothSourcesAndNamesOnly(t *testing.T) {
	s, _ := sealedStore(t)
	if err := s.Set("ext:memory", "on-disk", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Push("ext:memory", "pushed", "v2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Push("conn:matrix", "token", "v3"); err != nil {
		t.Fatal(err)
	}

	keys, err := s.Keys("ext:memory")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "on-disk" || keys[1] != "pushed" {
		t.Fatalf("keys = %v, want both sources sorted", keys)
	}
	scopes, err := s.Scopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[0] != "conn:matrix" || scopes[1] != "ext:memory" {
		t.Fatalf("scopes = %v", scopes)
	}
}

// Default DENY, and self-scope access without a grant. The second half is why
// grants do not immediately break everything: an extension reading back what it
// itself stored must not need to be granted access to its own scope.
func TestGrantsDefaultToDeny(t *testing.T) {
	s, _ := sealedStore(t)

	for _, tc := range []struct{ principal, scope string }{
		{"ext:memory", "core:provider.anthropic"},
		{"remote:builder-3", "ext:memory"},
		{"", "core:provider.anthropic"},
		{"ext:memory", ""},
	} {
		ok, err := s.Allows(tc.principal, tc.scope, ModeRead)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("%q reached %q with no grant", tc.principal, tc.scope)
		}
	}

	ok, err := s.Allows("ext:memory", "ext:memory", ModeRead)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("a principal cannot reach its own scope")
	}
}

func TestGrantModesAndExpiry(t *testing.T) {
	s, _ := sealedStore(t)
	const principal, scope = "remote:builder-3", "core:provider.anthropic"

	// use does NOT imply read: that is the whole point of the strong
	// delegation form — may ask the host to act, may not hold the material.
	if err := s.Grant(Grant{Principal: principal, Scope: scope, Mode: ModeUse}); err != nil {
		t.Fatal(err)
	}
	assertAllows(t, s, principal, scope, ModeUse, true)
	assertAllows(t, s, principal, scope, ModeRead, false)

	// read implies use.
	if err := s.Grant(Grant{Principal: principal, Scope: scope, Mode: ModeRead}); err != nil {
		t.Fatal(err)
	}
	assertAllows(t, s, principal, scope, ModeRead, true)
	assertAllows(t, s, principal, scope, ModeUse, true)

	// Re-granting replaced rather than accumulated.
	gs, err := s.Grants()
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 {
		t.Fatalf("grants = %d, want 1 after a re-grant", len(gs))
	}

	// An expired grant denies, but is still LISTED — a management surface has
	// to be able to show the thing you need to renew.
	if err := s.Grant(Grant{Principal: principal, Scope: scope, Mode: ModeRead,
		Expires: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	assertAllows(t, s, principal, scope, ModeRead, false)
	if gs, err := s.Grants(); err != nil || len(gs) != 1 {
		t.Fatalf("an expired grant vanished from the listing: %v %v", gs, err)
	}

	if err := s.Revoke(principal, scope); err != nil {
		t.Fatal(err)
	}
	if gs, err := s.Grants(); err != nil || len(gs) != 0 {
		t.Fatalf("after revoke: %v %v", gs, err)
	}
}

func TestGrantValidation(t *testing.T) {
	s, _ := sealedStore(t)
	for _, tc := range []struct {
		name string
		g    Grant
	}{
		{"no principal", Grant{Scope: "core:x", Mode: ModeRead}},
		{"no scope", Grant{Principal: "ext:a", Mode: ModeRead}},
		{"unknown mode", Grant{Principal: "ext:a", Scope: "core:x", Mode: "write"}},
		{"scope without a prefix", Grant{Principal: "ext:a", Scope: "bare", Mode: ModeRead}},
	} {
		if err := s.Grant(tc.g); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// Rotation must move the whole store onto the new key atomically, and must
// leave alone a store that is absent or still plaintext — rotation moves what
// is already sealed, and creating a file here would invent state.
func TestResealMovesTheStoreToANewKey(t *testing.T) {
	s, old := sealedStore(t)
	if err := s.Set("core:bot.telegram", "token", "tg-secret"); err != nil {
		t.Fatal(err)
	}

	next, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reseal(old, next.Recipient()); err != nil {
		t.Fatal(err)
	}

	// The old key no longer opens it; the new one does.
	reopened := New(s.Path(), secrets.NewCodec(func() (*age.X25519Identity, error) { return next, nil }))
	got, err := reopened.Get("core:bot.telegram", "token")
	if err != nil || got.Value != "tg-secret" {
		t.Fatalf("new key cannot read the resealed store: %+v %v", got, err)
	}
	stale := New(s.Path(), secrets.NewCodec(func() (*age.X25519Identity, error) { return old, nil }))
	if _, err := stale.Get("core:bot.telegram", "token"); err == nil {
		t.Error("the retired key still opens the store")
	}
}

func TestResealIgnoresAbsentAndPlaintextStores(t *testing.T) {
	next, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	old, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	absent := plainStore(t)
	if err := absent.Reseal(old, next.Recipient()); err != nil {
		t.Errorf("reseal of an absent store errored: %v", err)
	}
	if _, statErr := os.Stat(absent.Path()); !os.IsNotExist(statErr) {
		t.Error("reseal invented a store that did not exist")
	}

	plain := plainStore(t)
	if err := plain.Set("core:bot.telegram", "token", "tg-plain"); err != nil {
		t.Fatal(err)
	}
	if err := plain.Reseal(old, next.Recipient()); err != nil {
		t.Errorf("reseal of a plaintext store errored: %v", err)
	}
	raw, err := os.ReadFile(plain.Path())
	if err != nil {
		t.Fatal(err)
	}
	if secrets.IsAgeFile(raw) {
		t.Error("reseal encrypted a store that migrate was supposed to handle")
	}
}

// An encrypted store with no codec must say so, rather than surfacing as a JSON
// parse failure on ciphertext — the error a user can act on names the file.
func TestEncryptedStoreWithNoCodecSaysSo(t *testing.T) {
	s, _ := sealedStore(t)
	if err := s.Set("core:bot.telegram", "token", "tg-secret"); err != nil {
		t.Fatal(err)
	}
	blind := New(s.Path(), nil)
	_, err := blind.Get("core:bot.telegram", "token")
	if err == nil {
		t.Fatal("read an encrypted store with no codec")
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error does not explain the problem: %v", err)
	}
}

// Concurrent writers must not lose each other's values. The read-modify-write
// happens inside the lock; a save that read before acquiring it would silently
// drop whatever landed in between.
//
// Note what this does NOT pin: removing the in-process mutex leaves it passing,
// because the file lock serializes goroutines in one process too. The mutex
// still has a job — it guards the pushed map, and it is the ONLY serialization
// left on the degraded path where filelock.Acquire fails (a read-only mount) —
// but that path is not exercised here.
func TestConcurrentSetsDoNotLoseValues(t *testing.T) {
	s, _ := sealedStore(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Set("core:bot.telegram", string(rune('a'+i)), "v"); err != nil {
				t.Errorf("set: %v", err)
			}
		}()
	}
	wg.Wait()

	keys, err := s.Keys("core:bot.telegram")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 8 {
		t.Fatalf("kept %d of 8 concurrent writes: %v", len(keys), keys)
	}
}

// Unknown fields must round-trip, or a newer terva's state is destroyed by an
// older one reading and rewriting the file.
func TestUnknownFieldsSurviveARewrite(t *testing.T) {
	s := plainStore(t)
	if err := s.Set("core:bot.telegram", "token", "v"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if got := m["version"]; got != float64(formatVersion) {
		t.Errorf("version = %v, want %d", got, formatVersion)
	}
}

func assertAllows(t *testing.T, s *Store, principal, scope string, want Mode, expect bool) {
	t.Helper()
	ok, err := s.Allows(principal, scope, want)
	if err != nil {
		t.Fatal(err)
	}
	if ok != expect {
		t.Errorf("Allows(%q, %q, %q) = %v, want %v", principal, scope, want, ok, expect)
	}
}

// Forget's whole design is that it does two separable things, and only one of
// them destroys anything. A user unblocking a rotation (§8.13 Q10) asks for the
// first; getting the second would cost them a still-installed component's
// credential.
func TestForgetKeepsValuesUnlessPurged(t *testing.T) {
	store, _ := sealedStore(t)
	if err := store.Set("conn:matrix", "access_token", "syt-live"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("conn:matrix", "device_id", "DEV1"); err != nil {
		t.Fatal(err)
	}

	res, err := store.Forget("conn:matrix", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Values != 0 {
		t.Errorf("a forget without --purge must delete nothing, deleted %d", res.Values)
	}
	if res.Remaining != 2 {
		t.Errorf("remaining must count what was left so the caller can decide: got %d, want 2", res.Remaining)
	}
	// And they really are still there — a count is not the same as the values.
	if v, err := store.Get("conn:matrix", "access_token"); err != nil || v.Value != "syt-live" {
		t.Fatalf("forget destroyed a value it only counted: %+v %v", v, err)
	}

	res, err = store.Forget("conn:matrix", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Values != 2 || res.Remaining != 0 {
		t.Errorf("purge must delete every value and leave none: %+v", res)
	}
	if _, err := store.Get("conn:matrix", "access_token"); !errors.Is(err, ErrNotFound) {
		t.Errorf("purge left a value behind: %v", err)
	}
}

// A scope appears on BOTH sides of a grant — as the thing reached, and as the
// principal reaching. Forgetting a component that only ever appeared as a
// principal would otherwise leave a grant naming something that no longer
// exists, which reads as an authorization nobody can account for.
func TestForgetDropsGrantsNamingTheScopeOnEitherSide(t *testing.T) {
	store, _ := sealedStore(t)
	grants := []Grant{
		{Principal: "ext:memory", Scope: "conn:matrix", Mode: ModeRead},       // scope side
		{Principal: "conn:matrix", Scope: "core:bot.telegram", Mode: ModeUse}, // principal side
		{Principal: "ext:memory", Scope: "core:bot.telegram", Mode: ModeUse},  // neither: must survive
	}
	for _, g := range grants {
		if err := store.Grant(g); err != nil {
			t.Fatal(err)
		}
	}

	res, err := store.Forget("conn:matrix", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Grants != 2 {
		t.Errorf("both grants naming the scope must go: dropped %d, want 2", res.Grants)
	}
	left, err := store.Grants()
	if err != nil {
		t.Fatal(err)
	}
	// The surviving half is the assertion that keeps this from passing for the
	// wrong reason: a Forget that dropped EVERY grant would satisfy the count
	// above just as well.
	if len(left) != 1 || left[0].Principal != "ext:memory" || left[0].Scope != "core:bot.telegram" {
		t.Fatalf("an unrelated grant was collateral damage: %+v", left)
	}
}

// Forgetting a scope terva never knew about must be a no-op that SAYS so, not a
// silent success. An operator reading "done" after a typo concludes a rotation
// is unblocked when it is not.
func TestForgetOfAnUnknownScopeReportsNothing(t *testing.T) {
	store, _ := sealedStore(t)
	if err := store.Set("conn:matrix", "access_token", "syt-live"); err != nil {
		t.Fatal(err)
	}
	res, err := store.Forget("conn:matrixx", true)
	if err != nil {
		t.Fatal(err)
	}
	if res != (ForgetResult{}) {
		t.Errorf("a typo'd scope must report nothing removed, got %+v", res)
	}
	if v, err := store.Get("conn:matrix", "access_token"); err != nil || v.Value != "syt-live" {
		t.Fatalf("the near-miss scope was touched: %+v %v", v, err)
	}
}
