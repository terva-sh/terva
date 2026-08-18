package extconn

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/privfs"
)

// Pairing for connector extensions persists HOST-side, like external
// connectors' (chat/external/pairing.go) and for a sharper reason: the
// extension's own data dir is EXTENSION-writable, and a process must not
// be able to edit who it is paired to — pairing is host policy, applied
// before the extension's messages reach the agent. So the claim lives
// under a host-owned dir the extension never receives:
// $TERVA_HOME/ext-conn/<name>/pairing.json.
type pairingState struct {
	AllowedUserID string `json:"allowed_user_id,omitempty"`
}

func pairingPath(tervaHome, name string) string {
	return filepath.Join(tervaHome, "ext-conn", name, "pairing.json")
}

// loadPairing returns the persisted claim, "" when unpaired or the file
// is missing/corrupt (a broken pairing file degrades to re-pairing, not
// a dead connector).
func loadPairing(tervaHome, name string) string {
	b, err := os.ReadFile(pairingPath(tervaHome, name))
	if err != nil {
		return ""
	}
	var p pairingState
	if err := json.Unmarshal(b, &p); err != nil {
		return ""
	}
	return p.AllowedUserID
}

// savePairing persists a claim atomically, 0600 like every
// credential-adjacent file.
func savePairing(tervaHome, name, userID string) error {
	path := pairingPath(tervaHome, name)
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pairingState{AllowedUserID: userID}, "", "  ")
	if err != nil {
		return err
	}
	// Through privfs: same temp+rename, plus a chmod a permissive umask
	// cannot widen and a unique suffix that two concurrent savers cannot collide on.
	return privfs.WriteFile(path, b)
}

// removePairing deletes the pairing file; missing is fine.
func removePairing(tervaHome, name string) error {
	err := os.Remove(pairingPath(tervaHome, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
