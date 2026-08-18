package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"

	"terva.sh/terva/packages/privfs"
)

// The scene-backdrop store. Backgrounds are plain images a session binds for the
// Stage app to render behind the conversation — no parsing, no identity, just
// pixels — so this is a much simpler store than cards: one file per background at
// $TERVA_HOME/backgrounds/<id>.<ext>, the id a content hash (idempotent import).
// The /media/backgrounds/<id> route serves them; SessionMeta.Background binds one.

// BackgroundsDir is the on-disk background store, $TERVA_HOME/backgrounds.
func BackgroundsDir() string { return filepath.Join(config.TervaHome(), "backgrounds") }

// backgroundExts are the image formats a background may be stored as, detected
// by magic bytes at import so a client can't dictate the extension.
var backgroundExts = []string{"png", "jpg", "gif", "webp"}

// Background is a stored backdrop: its id and image extension.
type Background struct {
	ID  string
	Ext string
}

// BackgroundStore is the library rooted at BackgroundsDir().
type BackgroundStore struct{ dir string }

// NewBackgroundStore opens the store at the current $TERVA_HOME.
func NewBackgroundStore() *BackgroundStore { return &BackgroundStore{dir: BackgroundsDir()} }

// List returns every stored background, sorted by id.
func (s *BackgroundStore) List() ([]Background, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Background
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ext, ok := splitBackgroundName(e.Name())
		if !ok {
			continue
		}
		out = append(out, Background{ID: id, Ext: ext})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ImportBytes stores an image, returning its background. The format is detected
// from the bytes (not a client-supplied name); the id is a content hash, so a
// re-import is idempotent.
func (s *BackgroundStore) ImportBytes(data []byte) (Background, error) {
	ext, ok := detectImageExt(data)
	if !ok {
		return Background{}, fmt.Errorf("background: unrecognized image format (want png/jpeg/gif/webp)")
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])[:16]
	if err := privfs.MkdirAll(s.dir); err != nil {
		return Background{}, err
	}
	if err := privfs.WriteFileMode(filepath.Join(s.dir, id+"."+ext), data, 0o644); err != nil {
		return Background{}, err
	}
	return Background{ID: id, Ext: ext}, nil
}

// ImportPath stores an image from a server-local path.
func (s *BackgroundStore) ImportPath(path string) (Background, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Background{}, err
	}
	return s.ImportBytes(data)
}

// Path returns the filesystem path of a background by id, or "" if absent or the
// id is invalid. Used by the /media route.
func (s *BackgroundStore) Path(id string) string {
	if !validBackgroundID(id) {
		return ""
	}
	for _, ext := range backgroundExts {
		p := filepath.Join(s.dir, id+"."+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// Delete removes a background by id.
func (s *BackgroundStore) Delete(id string) error {
	if !validBackgroundID(id) {
		return fmt.Errorf("invalid background id %q", id)
	}
	removed := false
	for _, ext := range backgroundExts {
		p := filepath.Join(s.dir, id+"."+ext)
		if err := os.Remove(p); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if !removed {
		return fmt.Errorf("background %q not found", id)
	}
	return nil
}

// detectImageExt sniffs a supported image format from its magic bytes, returning
// the stored extension. png/jpeg/gif/webp — the formats a browser renders and
// generate_image emits.
func detectImageExt(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "png", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpg", true
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "gif", true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp", true
	}
	return "", false
}

// splitBackgroundName splits "<id>.<ext>" and validates both halves.
func splitBackgroundName(name string) (id, ext string, ok bool) {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 {
		return "", "", false
	}
	id, ext = name[:dot], name[dot+1:]
	if !validBackgroundID(id) {
		return "", "", false
	}
	for _, e := range backgroundExts {
		if ext == e {
			return id, ext, true
		}
	}
	return "", "", false
}

// validBackgroundID accepts only the content-hash ids the store mints (hex),
// which by construction cannot escape the store directory.
func validBackgroundID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
