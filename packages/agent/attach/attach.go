// Package attach moves files between the user and the agent, in both
// directions, through per-session areas under $TERVA_HOME that it owns entirely.
//
// INBOUND ($TERVA_HOME/attachments, [NewStore]). A client — today the web
// control panel — can upload any file, not just an image. Images ride the turn
// inline as base64 because vision needs the bytes in the message; everything
// else is too large for that and, more importantly, is not something the model
// should have to hold in context: it wants a PATH it can read, grep, or copy
// into the workspace. packages/agent/build registers this root as a sandbox
// read-only root, and read-only is the whole grant — the agent reads a staged
// file and writes its own copy into the workspace.
//
// OUTBOUND ($TERVA_HOME/shared, [NewShareStore]). The agent hands a file back to
// the human through the share_file tool, and the panel renders it as something
// to look at, play, or download. This root is deliberately NOT a sandbox root
// (see share.go): the agent publishes THROUGH the tool, so it has no reason to
// read the area and no business enumerating what other sessions have shared.
//
// Both directions share everything below the policy line — id minting, name
// sanitizing, mime/kind derivation, per-session dirs, and the TTL+cap sweeper —
// and differ only in their [Policy] and in how bytes arrive ([Store.Stage] from
// a reader, [Store.Publish] from a path). Nothing in either area is the agent's
// to delete: Sweep is, which is why a message can name a file long after the
// bytes are gone without that being a bug.
package attach

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
)

// DirName is the staging root's name under $TERVA_HOME. Exported because
// packages/agent/build must grant the sandbox the same directory this package
// writes to, and a literal spelled in both places is a drift waiting to happen.
const DirName = "attachments"

const (
	// MaxBytes bounds a single staged file. Rejected, never truncated: half an
	// email export or half a database dump is silent corruption, and unlike a
	// wire frame there is no stream to keep alive by skipping it.
	MaxBytes int64 = 100 << 20

	// TTL is how long a staged file survives. Long enough to outlive a daemon
	// restart mid-conversation, short enough that a scratch space stays scratch.
	TTL = 24 * time.Hour

	// CapBytes bounds the whole staging area across every session. At MaxBytes
	// per file that is ~20 of the largest uploads; ordinary ones never approach it.
	CapBytes int64 = 2 << 30

	// Grace protects a freshly staged file from cap eviction. Without it a burst
	// of uploads could evict the very files the turn being composed is about to
	// reference — the one deletion that is never recoverable by re-reading.
	Grace = time.Hour

	// SweepInterval is how often the background sweeper runs. The TTL is the
	// contract; this is only how promptly it is honored.
	SweepInterval = time.Hour

	// idBytes is the entropy in an id. These are not secrets — the directory is
	// 0700 and the download route is auth-gated — but ids must not collide
	// within a session.
	idBytes = 8
)

// Policy is one area's retention rule. It is per-STORE rather than per-package
// because the two directions want genuinely different answers: an inbound file
// has done its job once the agent has read it, while a shared one is the
// deliverable, and the user may come back for it days later.
//
// Sweep still takes its bounds as arguments — tests drive them directly — so
// this is what the background sweeper applies, not a second source of truth.
type Policy struct {
	// TTL is how long a file survives regardless of how much room there is.
	TTL time.Duration
	// CapBytes bounds the whole area across every session. Enforced after the
	// TTL, oldest first.
	CapBytes int64
	// Grace protects a freshly written file from cap eviction.
	Grace time.Duration
}

// StagePolicy is the inbound area's retention rule.
var StagePolicy = Policy{TTL: TTL, CapBytes: CapBytes, Grace: Grace}

// ErrTooLarge reports a file that exceeded the store's size limit. A partial
// write is removed before it is returned.
var ErrTooLarge = errors.New("file exceeds the size limit")

// ErrNotFound reports an id that names nothing in the session's staging dir —
// swept, never staged, or from another session.
var ErrNotFound = errors.New("attachment not found")

// Ref is one staged file, as the model will be told about it.
//
// It is reconstructed from the path rather than from a stored sidecar, so it
// cannot disagree with what is actually on disk: Name is the (sanitized) name
// the file has, Size is its current size, and Mime is derived from the
// extension or sniffed from the bytes. A client's declared content type is
// deliberately not trusted or retained — it is the one field a caller can lie
// about, and nothing downstream needs the lie.
type Ref struct {
	ID       string
	Name     string
	Mime     string
	Kind     string // image | audio | video | document
	Size     int64
	Duration time.Duration // non-zero only for connector-supplied media
	Path     string
	// ExpiresAt is when this store's sweeper becomes entitled to remove the
	// file: its modification time plus the store's TTL, which is the same
	// arithmetic Sweep performs, read off the same clock.
	//
	// An UPPER BOUND, not a promise. Cap eviction can take a file before this,
	// and a store assembled without a policy leaves it zero. A reader may say
	// "gone by now" and be right; it may not say "still there".
	ExpiresAt time.Time
}

// Store is one per-session file area rooted at a directory it owns entirely.
// Both directions are the same type with different fields; see the package doc.
type Store struct {
	root string
	// label names what this store holds, for error text and the sweeper's log
	// line. A daemon operator reading "shared file write: no space left" should
	// not have to work out which of the two areas filled up.
	label string
	// idPrefix tags this store's ids, so an id that turns up in a log or a wire
	// frame says which area it came from and a cross-store mixup is visible
	// rather than a silent ErrNotFound.
	idPrefix string
	// policy is what StartSweeper enforces.
	policy Policy
	// maxBytes bounds one file. Always MaxBytes in production; the package's own
	// tests lower it so the rejection and boundary paths can be exercised
	// without a 100 MB write on every CI run.
	maxBytes int64
}

// NewStore returns the inbound staging store at $TERVA_HOME/attachments.
func NewStore() *Store { return NewStoreAt(filepath.Join(config.TervaHome(), DirName)) }

// NewStoreAt returns an inbound staging store rooted at an explicit directory.
// For tests and for callers that resolve $TERVA_HOME themselves.
func NewStoreAt(root string) *Store {
	return &Store{
		root:     root,
		label:    "attachment",
		idPrefix: "att_",
		policy:   StagePolicy,
		maxBytes: MaxBytes,
	}
}

// Root is the directory the store owns. The inbound root is registered as a
// sandbox read-only root, so everything beneath it is readable by a jailed agent
// and writable by none; the share root is registered as neither.
func (s *Store) Root() string { return s.root }

// SessionDir is where one session's files live.
//
// A session id that sanitizes to nothing (empty, or all dots) falls back to a
// fixed bucket rather than to the empty string, because filepath.Join would
// then hand back the root itself — collapsing every such session onto the
// directory that holds all the others.
func (s *Store) SessionDir(sess string) string {
	clean := sanitizeSegment(sess)
	if clean == "" {
		clean = "_"
	}
	return filepath.Join(s.root, clean)
}

// Stage streams r into the session's staging dir and returns the Ref for it.
//
// The reader is bounded at MaxBytes+1 so an over-limit upload is detected rather
// than silently truncated; the partial file is removed before returning
// ErrTooLarge. name is only ever a suggestion — sanitizeSegment decides what
// actually lands on disk.
func (s *Store) Stage(sess, name string, r io.Reader) (Ref, error) {
	dir := s.SessionDir(sess)
	if err := privfs.MkdirAll(dir); err != nil {
		return Ref{}, fmt.Errorf("%s dir: %w", s.label, err)
	}
	id, err := s.newID()
	if err != nil {
		return Ref{}, err
	}
	path := filepath.Join(dir, id+"-"+sanitizeName(name))
	f, err := privfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return Ref{}, fmt.Errorf("%s create: %w", s.label, err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(r, s.maxBytes+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		_ = os.Remove(path)
		return Ref{}, fmt.Errorf("%s write: %w", s.label, copyErr)
	case closeErr != nil:
		_ = os.Remove(path)
		return Ref{}, fmt.Errorf("%s write: %w", s.label, closeErr)
	case n > s.maxBytes:
		_ = os.Remove(path)
		return Ref{}, ErrTooLarge
	}
	// Stat rather than time.Now(): the sweeper ages this file by its mtime, and
	// on a filesystem with coarse timestamps the two differ by enough to matter
	// at the boundary. A stat that fails leaves the expiry zero, which reads as
	// "unknown" rather than as a wrong answer.
	var mod time.Time
	if info, err := os.Stat(path); err == nil {
		mod = info.ModTime()
	}
	return s.refFor(path, id, n, mod), nil
}

// Resolve returns the Ref for an id previously staged in this session, or
// ErrNotFound. It never consults a caller-supplied path: the id is matched
// against the session directory's own entries, so neither a traversal in the id
// nor a reference to another session's file can resolve.
func (s *Store) Resolve(sess, id string) (Ref, error) {
	clean := sanitizeSegment(id)
	if clean == "" || clean != id {
		return Ref{}, ErrNotFound
	}
	entries, err := os.ReadDir(s.SessionDir(sess))
	if err != nil {
		return Ref{}, ErrNotFound
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), clean+"-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return Ref{}, ErrNotFound
		}
		return s.refFor(filepath.Join(s.SessionDir(sess), e.Name()), clean, info.Size(), info.ModTime()), nil
	}
	return Ref{}, ErrNotFound
}

// ResolveAll maps ids to Refs, silently skipping those that no longer resolve,
// and reports how many were missing.
//
// A vanished attachment is expected, not exceptional: the sweeper is allowed to
// reap a file while the message that named it lives on. Failing the whole send
// because one id expired would make a stale composer unusable, so the caller
// gets what survives plus a count it can mention in the turn.
func (s *Store) ResolveAll(sess string, ids []string) (refs []Ref, missing int) {
	for _, id := range ids {
		ref, err := s.Resolve(sess, id)
		if err != nil {
			missing++
			continue
		}
		refs = append(refs, ref)
	}
	return refs, missing
}

// refFor reconstructs a Ref from what is on disk.
// mod is the file's own modification time — the value Sweep ages it against —
// so ExpiresAt is that same decision computed ahead of time rather than a second
// opinion about it.
func (s *Store) refFor(path, id string, size int64, mod time.Time) Ref {
	name := strings.TrimPrefix(filepath.Base(path), id+"-")
	mimeType := detectMime(path)
	ref := Ref{
		ID:   id,
		Name: name,
		Mime: mimeType,
		Kind: KindFor(mimeType),
		Size: size,
		Path: path,
	}
	// A zero TTL means no policy was set (a store built by a struct literal in a
	// test); leave the expiry zero rather than claiming everything expired at
	// the epoch, which reads as "gone" to every caller.
	if s.policy.TTL > 0 && !mod.IsZero() {
		ref.ExpiresAt = mod.Add(s.policy.TTL)
	}
	return ref
}

// mediaTypes pins the audio and video types this package must answer the same
// way on every host, because neither of the two sources below can be relied on
// to.
//
// Go's built-in table has NO audio or video entries at all — it knows .png and
// .pdf and nothing that plays — so mime.TypeByExtension answers only from the
// SYSTEM database, which a scratch or distroless container does not have. That
// is exactly how a self-hosted terva gets deployed, and there every shared clip
// fell through to sniffing; a sniff that misses turns a player into a download.
// CI caught it on .mp4 while macOS, which has the system table, passed.
//
// Where a system table does exist its spellings vary, which is the second half
// of the same problem: macOS answers audio/x-wav and audio/x-flac, neither of
// which any browser-facing allowlist spells that way. A .wav share therefore
// failed to play on the machine that "worked" too.
//
// Kept deliberately wider than the inline allowlist in packages/agent/web:
// deriving a type and deciding what may render are different questions, and a
// .mov the panel declines to play is still a video in the manifest the model
// reads.
var mediaTypes = map[string]string{
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".oga":  "audio/ogg",
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".wav":  "audio/wav",
	".flac": "audio/flac",
	".weba": "audio/webm",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
	".ogv":  "video/ogg",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
}

// detectMime prefers the extension and falls back to sniffing the leading
// bytes, which is what http.DetectContentType is for. An unreadable or empty
// file yields "", and the manifest simply omits the type rather than asserting
// application/octet-stream about a file it could not read.
func detectMime(path string) string {
	if ext := strings.ToLower(filepath.Ext(path)); ext != "" {
		// Ours first: the host's answer for these is absent or differently
		// spelled often enough that consulting it would make the result depend
		// on which machine the daemon happens to run on.
		if t, ok := mediaTypes[ext]; ok {
			return t
		}
		if t := mime.TypeByExtension(ext); t != "" {
			return strings.TrimSpace(strings.Split(t, ";")[0])
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var head [512]byte
	n, _ := io.ReadFull(f, head[:])
	if n == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Split(http.DetectContentType(head[:n]), ";")[0])
}

// KindFor maps a mime type onto the attachment kind vocabulary shared with the
// connector wire (connsdk.Attachment.Kind), so one manifest renderer serves both.
func KindFor(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	default:
		return "document"
	}
}

// Manifest renders the block that tells the model what was attached and where.
//
// This is the single renderer for every inbound attachment path — the chat
// connectors' staged files and the web client's uploads both come through here,
// so the model sees one format and a fix lands in one place.
//
// missing counts attachments the message named that no longer resolve. They are
// reported rather than omitted: the model is about to be asked about files, and
// silently showing it fewer than the user attached invites a confident answer
// about the wrong set. Connector callers pass 0 — their files are moved into
// place, not resolved by id, so there is nothing to have expired.
func Manifest(refs []Ref, missing int) string {
	if len(refs) == 0 && missing == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(i18n.P("chat.attachment_preamble", "(the user attached files, saved locally — read them with your tools if relevant:"))
	b.WriteByte('\n')
	for _, r := range refs {
		fmt.Fprintf(&b, "  %s — %s", flatten(r.Path), r.Kind)
		if r.Mime != "" {
			fmt.Fprintf(&b, ", %s", r.Mime)
		}
		if r.Size > 0 {
			fmt.Fprintf(&b, ", %d bytes", r.Size)
		}
		if r.Duration > 0 {
			fmt.Fprintf(&b, ", %s", r.Duration.Round(100*time.Millisecond))
		}
		b.WriteByte('\n')
	}
	if missing > 0 {
		b.WriteString(i18n.P("chat.attachment_expired",
			"  (%d further attachment(s) are no longer on disk — ask the user to re-attach them if you need them)", missing))
		b.WriteByte('\n')
	}
	b.WriteString(")\n")
	return b.String()
}

// flatten collapses a rendered path onto a single line.
//
// A staged path ends in a user-supplied filename, and it lands in the model's
// context, so a name carrying "\n)" could forge the end of the manifest block
// and make whatever followed read as the user's own words. Files staged by this
// package are already character-filtered by sanitizeName; this guards the
// connector path, which keeps whatever filepath.Base left of a name a chat
// service supplied.
func flatten(path string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, path))
}

// newID returns a fresh id carrying this store's prefix.
func (s *Store) newID() (string, error) {
	var buf [idBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("%s id: %w", s.label, err)
	}
	return s.idPrefix + hex.EncodeToString(buf[:]), nil
}

// sanitizeSegment reduces a caller-supplied string to one boring path segment.
// Anything outside [A-Za-z0-9._-] becomes "_", so no separator, no "..", and no
// control character can survive into a path. A segment that is only dots is
// rejected outright ("" ), since "." and ".." are the two names that would
// still mean something to the filesystem after character filtering.
func sanitizeSegment(s string) string {
	if s == "" || strings.Trim(s, ".") == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sanitizeName turns a client's filename into the trailing part of the on-disk
// name. Only the base is kept (a client sending "../../etc/passwd" contributes
// "passwd"), a leading dot is dropped so nothing lands hidden, and the result is
// bounded so a pathological name cannot blow the filesystem's name limit. An
// empty result becomes "file" — the id prefix is what makes the name unique.
func sanitizeName(name string) string {
	clean := sanitizeSegment(filepath.Base(strings.TrimSpace(name)))
	clean = strings.TrimLeft(clean, ".")
	if len(clean) > 120 {
		clean = clean[:120]
	}
	if clean == "" {
		return "file"
	}
	return clean
}
