package restartmarker

import (
	"os"
	"runtime"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func TestArmReadConsume(t *testing.T) {
	home := testsupport.TempDir(t)
	now := time.Unix(1_000_000, 0)

	// Nothing armed yet.
	if _, ok := Read(home, now); ok {
		t.Fatal("Read on an empty home returned ok")
	}

	m := Marker{Session: "sess-abc", FromVersion: "0.126.8", Reason: "apply unit change", ExpiresUnix: now.Add(10 * time.Second).Unix()}
	if err := Arm(home, m); err != nil {
		t.Fatal(err)
	}

	got, ok := Read(home, now)
	if !ok {
		t.Fatal("Read after Arm returned !ok")
	}
	if got.Session != "sess-abc" || got.FromVersion != "0.126.8" || got.Reason != "apply unit change" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Nonce == "" {
		t.Error("Arm did not fill a nonce")
	}

	// Read does not remove; Consume does.
	if _, ok := Read(home, now); !ok {
		t.Error("Read removed the marker (should be non-destructive)")
	}
	c, ok := Consume(home, now)
	if !ok || c.Session != "sess-abc" {
		t.Errorf("Consume = %+v ok=%v, want the marker", c, ok)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Error("Consume left the marker file on disk")
	}
	if _, ok := Read(home, now); ok {
		t.Error("Read after Consume returned ok")
	}
}

func TestExpiredMarkerIsInvalidAndCleared(t *testing.T) {
	home := testsupport.TempDir(t)
	armed := time.Unix(2_000_000, 0)
	m := Marker{Session: "s1", ExpiresUnix: armed.Add(5 * time.Second).Unix()}
	if err := Arm(home, m); err != nil {
		t.Fatal(err)
	}

	// Within the window: valid.
	if _, ok := Read(home, armed.Add(4*time.Second)); !ok {
		t.Error("marker read as invalid inside its window")
	}
	// After expiry: invalid.
	after := armed.Add(6 * time.Second)
	if _, ok := Read(home, after); ok {
		t.Error("expired marker read as valid")
	}
	// Consume of an expired marker reports !ok but still clears the file, so a
	// stale marker cannot linger to mislabel a later stop.
	if _, ok := Consume(home, after); ok {
		t.Error("Consume of an expired marker returned ok")
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Error("Consume left an expired marker on disk")
	}
}

func TestMarkerWithoutSessionIsInvalid(t *testing.T) {
	home := testsupport.TempDir(t)
	now := time.Unix(3_000_000, 0)
	if err := Arm(home, Marker{ExpiresUnix: now.Add(time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Read(home, now); ok {
		t.Error("a sessionless marker read as valid")
	}
}

func TestMarkerFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file-mode assertions do not apply on Windows")
	}
	home := testsupport.TempDir(t)
	if err := Arm(home, Marker{Session: "s", ExpiresUnix: time.Unix(9_000_000, 0).Unix()}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("marker mode = %#o, want 0600 (privfs-written)", got)
	}
}

func TestNonceIsRandomish(t *testing.T) {
	if NewNonce() == NewNonce() {
		t.Error("two nonces collided")
	}
}
