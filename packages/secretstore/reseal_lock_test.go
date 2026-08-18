package secretstore

import (
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

// Reseal read-modify-writes secrets.json and took only s.mu.
//
// s.mu orders nothing that matters. config.SecretStoreIn mints a FRESH Store
// per call, so two callers in one process hold two different mutexes over one
// file — and across processes it means nothing at all, which is the case
// filelock exists for. A rotation racing an ordinary Set read the pre-Set
// bytes, re-sealed those, and published them over the Set: the secret that had
// just been stored was gone, and the atomic rename is exactly what stops anyone
// noticing.
//
// The deterministic shape: park the lock, assert Reseal BLOCKS, release, assert
// it RETURNS. The blocking half is what rules out a no-op — a Reseal that never
// touches the lock would sail through step one.
func TestResealWaitsForTheCrossProcessLock(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "secrets.json")

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	// A sealed store with something in it.
	s := New(path, sealingCodec(id))
	if err := s.Set("core:openai", "api_key", "sk-live"); err != nil {
		t.Fatal(err)
	}

	// Park the lock, as another process holding it would.
	held, err := filelock.Acquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		other := New(path, sealingCodec(id))
		done <- other.Reseal(id, id.Recipient())
	}()

	select {
	case err := <-done:
		t.Fatalf("Reseal completed while another writer held the lock (err=%v) — it read and "+
			"republished the file with no interlock at all", err)
	case <-time.After(250 * time.Millisecond):
		// Blocked, which is the point.
	}

	held.Release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Reseal failed once the lock was free: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reseal never completed after the lock was released — it is waiting on something else")
	}

	// And the secret survived the rotation.
	got, err := New(path, sealingCodec(id)).Get("core:openai", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "sk-live" {
		t.Errorf("after reseal the value is %q, want %q", got.Value, "sk-live")
	}
}

// A home that cannot host a lockfile must still rotate — mutate accepts that
// degradation and Reseal must not be stricter, or a read-only-adjacent home
// becomes one where rotation fails outright.
func TestResealStillWorksWhereNoLockfileCanExist(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "secrets.json")

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	s := New(path, sealingCodec(id))
	if err := s.Set("core:anthropic", "api_key", "sk-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reseal(id, id.Recipient()); err != nil {
		t.Fatalf("reseal on an ordinary home failed: %v", err)
	}
	if got, _ := s.Get("core:anthropic", "api_key"); got.Value != "sk-a" {
		t.Errorf("value = %q after reseal", got.Value)
	}
}

// sealingCodec is a codec that seals to (and opens with) one identity.
func sealingCodec(id *age.X25519Identity) *secrets.Codec {
	return secrets.NewCodec(func() (*age.X25519Identity, error) { return id, nil })
}
