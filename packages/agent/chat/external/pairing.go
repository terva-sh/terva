package external

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/privfs"
)

// Pairing for external connectors persists HOST-side, under the
// connector's state dir — an improvement on the in-tree split: the
// host never sees service tokens; the connector never sees pairing
// policy.
type pairingState struct {
	AllowedUserID string `json:"allowed_user_id,omitempty"`
}

func pairingPath(tervaHome, name string) string {
	return filepath.Join(ConnectorsDir(tervaHome), name, "pairing.json")
}

// loadPairing returns the persisted claim, "" when unpaired or the
// file is missing/corrupt (a broken pairing file degrades to
// re-pairing, not a dead connector).
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// removePairing deletes the pairing file; missing is fine.
func removePairing(tervaHome, name string) error {
	err := os.Remove(pairingPath(tervaHome, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
