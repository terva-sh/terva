package talkoot

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The room is sealed. Every line carries an HMAC-SHA256 over its own bytes
// and the MAC of the line before it, under a key only the daemon holds, so
// the lines form a chain.
//
// 🔑 Replay trusts the room: a resume line lifts a cap, a turn line sets the
// day's spend, and an answer line stands for a person's decision. A member
// with a shell can append to the file, because the jail confines the write
// tools and not bash (docs/permissions.md). The seal makes a line the router
// did not write fail on replay, and a failed line pauses the talkoot.
//
// ⚠️ The key sits beside the room, and the sandbox denies it by name
// (build.restrictSensitiveReads). That is a speed bump, not a boundary: a
// member running as the same OS user can still reach the file by a path the
// deny list does not match, or swap in a whole directory with a key of its
// own. TKT-01M3AMK0M9 records the limits.

// KeyFile and HeadFile sit beside RoomFile.
const (
	KeyFile  = "room.key"
	HeadFile = "room.head"
	// HeadTempFile is where the head is written before its rename. It holds a
	// whole sealed head, so the sandbox denies it by name like the head.
	HeadTempFile = "room.head.tmp"
)

const keyHeader = "terva talkoot room key v1"

// macPattern matches the seal at the end of a line. The MAC is the last field
// Append writes, so the rest of the line is exactly the bytes it sealed.
var macPattern = regexp.MustCompile(`,"mac":"([0-9a-f]{64})"\}$`)

// roomKey is the key a room is sealed under. id names it in each line, so a
// future rotation can tell which key sealed which line.
type roomKey struct {
	secret []byte
	id     string
}

func (k roomKey) mac(parts ...[]byte) string {
	h := hmac.New(sha256.New, k.secret)
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// lineMAC seals one line's bytes to the line before it.
func (k roomKey) lineMAC(prev string, body []byte) string {
	return k.mac([]byte("talkoot-room-line"), []byte(prev), body)
}

// seal returns the line as written: the body with the MAC as its last field.
func (k roomKey) seal(prev string, body []byte) ([]byte, string) {
	m := k.lineMAC(prev, body)
	out := make([]byte, 0, len(body)+len(m)+10)
	out = append(out, body[:len(body)-1]...)
	out = append(out, `,"mac":"`...)
	out = append(out, m...)
	out = append(out, `"}`...)
	return out, m
}

// unseal splits a written line into the body it sealed and the MAC it
// claims. ok is false for a line with no seal, such as a torn one.
func unseal(line []byte) (body []byte, claimed string, ok bool) {
	m := macPattern.FindSubmatchIndex(line)
	if m == nil {
		return nil, "", false
	}
	body = append(append([]byte{}, line[:m[0]]...), '}')
	return body, string(line[m[2]:m[3]]), true
}

// loadKey reads the room's key. A missing key is not an error: created
// reports that a new one was made, and the caller decides what that means for
// lines already in the room.
func loadKey(dir string) (k roomKey, created bool, err error) {
	path := filepath.Join(dir, KeyFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return createKey(path)
	}
	if err != nil {
		return roomKey{}, false, fmt.Errorf("talkoot: room key: %w", err)
	}
	head, rest, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	secret, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
	if strings.TrimSpace(head) != keyHeader || derr != nil || len(secret) != 32 {
		// 🚨 A key that does not parse cannot verify anything, and a new key
		// would accept whatever is appended next. Stop rather than guess.
		return roomKey{}, false, fmt.Errorf("talkoot: room key %s does not parse; restore it, or move the room aside to start a new one", path)
	}
	return newRoomKey(secret), false, nil
}

func createKey(path string) (roomKey, bool, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return roomKey{}, false, fmt.Errorf("talkoot: room key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return roomKey{}, false, fmt.Errorf("talkoot: room key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return roomKey{}, false, fmt.Errorf("talkoot: room key: %w", err)
	}
	_, err = fmt.Fprintf(f, "%s\n%s\n", keyHeader, base64.StdEncoding.EncodeToString(secret))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return roomKey{}, false, fmt.Errorf("talkoot: room key: %w", err)
	}
	return newRoomKey(secret), true, nil
}

func newRoomKey(secret []byte) roomKey {
	sum := sha256.Sum256(secret)
	return roomKey{secret: secret, id: hex.EncodeToString(sum[:8])}
}

// head names the last line the router wrote. The chain alone cannot see lines
// cut from the end, because what remains is still a valid chain. The head can:
// its line must be in the room.
type head struct {
	Count int    `json:"count"`
	Last  string `json:"last"`
	Kid   string `json:"kid"`
	MAC   string `json:"mac"`
}

func (k roomKey) headMAC(h head) string {
	return k.mac([]byte("talkoot-room-head"), []byte(strconv.Itoa(h.Count)), []byte(h.Last), []byte(h.Kid))
}

// writeHead replaces the head file. It writes a temporary file and renames it,
// so a crash leaves the old head or the new one, never half of either.
//
// 🚨 The temporary name is fixed, so the deny list can name it: a random name
// would hand a member a sealed head at every write. Whatever sits there is
// removed first, and the file is created exclusively, so a planted symlink is
// never followed. A planted directory that is not empty fails the write, and
// with it the append. That only denies service, which a shell can do anyway.
func writeHead(dir string, k roomKey, count int, last string) error {
	h := head{Count: count, Last: last, Kid: k.id}
	h.MAC = k.headMAC(h)
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, HeadTempFile)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, HeadFile))
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// CreateRoom makes the key and the head for a new talkoot's room. The path
// that creates a talkoot calls it. A room opened without a key gets one too,
// but it starts paused, because a missing key can also mean a room that was
// deleted to clear its spend and its pauses.
func CreateRoom(dir string) error {
	k, created, err := loadKey(dir)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("talkoot: %s already has a room key", dir)
	}
	if err := writeHead(dir, k, 0, ""); err != nil {
		// Without the key, a retry starts over rather than finding a room
		// that is half made.
		_ = os.Remove(filepath.Join(dir, KeyFile))
		return fmt.Errorf("talkoot: room head: %w", err)
	}
	return nil
}

// readHead returns the head, or a reason it cannot be trusted. A missing head
// is a reason too: the key and the head are written together.
func readHead(dir string, k roomKey) (head, string) {
	b, err := os.ReadFile(filepath.Join(dir, HeadFile))
	if errors.Is(err, os.ErrNotExist) {
		return head{}, "the room's head record is missing"
	}
	if err != nil {
		return head{}, "the room's head record cannot be read: " + err.Error()
	}
	var h head
	if json.Unmarshal(bytes.TrimSpace(b), &h) != nil || h.Kid != k.id ||
		!hmac.Equal([]byte(h.MAC), []byte(k.headMAC(h))) {
		return head{}, "the room's head record fails its seal"
	}
	return h, ""
}
