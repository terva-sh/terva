package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"terva.sh/terva/packages/provider"
)

// PortableExt is the filesystem extension WRITTEN for exported
// sessions. A ".tervasession" is just a terva JSONL session file with the
// meta header rewritten so the importing user gets fresh ownership.
//
// Reads accept both this and the renamed ".tervasession" spelling —
// dual-read is forever-cheap for user data (docs/plans/rename-terva.md).
// Import never gated on the extension anyway (it validates the meta
// header), so the read seam only affects export's "already has the
// extension" checks below.
const PortableExt = ".tervasession"

// portableExts are the extensions recognized as portable sessions,
// in either naming era.
var portableExts = []string{".tervasession", ".zotsession"} // rename:keep — dual-read forever

// hasPortableExt reports whether path already carries a recognized
// portable-session extension (either era), so export doesn't stack
// a second one onto an explicit destination name.
func hasPortableExt(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range portableExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// ExportSession writes the session at srcPath to dstPath as a
// portable .tervasession file. If dstPath is an existing directory the
// file is created inside it with a name derived from the session's
// meta ("YYYYMMDD-HHMMSS-<first-prompt-excerpt>.tervasession"). The
// destination's directory is created if needed. Returns the final
// resolved path so the caller can tell the user where it landed.
//
// The on-disk format is unchanged from a live session; only the
// meta.cwd is stripped of its per-machine prefix (the importing
// user doesn't care what directory it came from). Everything else
// round-trips byte-for-byte.
func ExportSession(srcPath, dstPath string) (string, error) {
	if srcPath == "" {
		return "", errors.New("export: source path is empty")
	}
	if dstPath == "" {
		return "", errors.New("export: destination path is empty")
	}

	// Read the source meta up-front so we can name the output sensibly
	// when dstPath is a directory, and so we can validate it's a real
	// session before starting to write.
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("export: open source: %w", err)
	}
	defer src.Close()

	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 20*1024*1024)
	if !sc.Scan() {
		return "", errors.New("export: session file is empty")
	}
	var head sessionLine
	if err := json.Unmarshal(sc.Bytes(), &head); err != nil {
		return "", fmt.Errorf("export: parse meta: %w", err)
	}
	if head.Type != "meta" || head.Meta == nil {
		return "", errors.New("export: first line is not a meta row")
	}

	// Scan the rest of the file for the first user message so we can
	// build a humane filename. Only reads if dstPath doesn't already
	// end in .tervasession.
	firstPrompt := ""
	if !hasPortableExt(dstPath) {
		if fi, _ := os.Stat(dstPath); fi == nil || fi.IsDir() {
			p, err := firstUserPrompt(src)
			if err != nil {
				return "", fmt.Errorf("export: read first prompt: %w", err)
			}
			firstPrompt = p
		}
	}

	// Resolve dstPath: if it's a directory, build a name inside it.
	outPath := dstPath
	if fi, err := os.Stat(dstPath); err == nil && fi.IsDir() {
		name := filenameFor(head.Meta.Started, head.Meta.ID, firstPrompt)
		outPath = filepath.Join(dstPath, name)
	} else if !hasPortableExt(outPath) {
		outPath += PortableExt
	}

	// Re-open the source from the top since we advanced the scanner.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("export: rewind: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", fmt.Errorf("export: mkdir dst: %w", err)
	}
	dst, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("export: create dst: %w", err)
	}
	defer dst.Close()
	bw := bufio.NewWriter(dst)

	// Stream every row, rewriting the meta ones. Use ReadBytes instead of
	// bufio.Scanner: large sessions can contain very long JSONL rows
	// (image blocks, big tool outputs, compacted history) that exceed
	// Scanner's token limit and fail with "token too long".
	//
	// EVERY meta row, not just the first. They are an append-only, last-wins
	// timeline — SetCreationSpec writes the second one, and a session's mode,
	// card, cast, greeting, lorebook, note, background, bound user, and every
	// model switch are all written after creation. Keeping only the first
	// exported a session's birth certificate instead of the session: an
	// exported --play session imported as a plain coding session, with its
	// World lore gone.
	//
	// What actually had to be removed is the CWD, which every meta row carries
	// and which belongs to the exporting user. So it is stripped per row —
	// dropping the rows was a blunt way to achieve that, and it took the state
	// with it. IDs stay so the export is traceable; the importer rotates them.
	r := bufio.NewReader(src)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			var h sessionLineHead
			if uerr := json.Unmarshal(line, &h); uerr == nil {
				out := line
				if h.Type == "meta" {
					if out, uerr = metaRowWithoutCWD(line); uerr != nil {
						// A meta row we cannot re-marshal is dropped rather
						// than emitted with the cwd still in it. Losing one
						// row's state beats leaking a path.
						out = nil
					}
				}
				if len(out) > 0 {
					if _, werr := bw.Write(out); werr != nil {
						return "", werr
					}
					if werr := bw.WriteByte('\n'); werr != nil {
						return "", werr
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("export: read source: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		return "", err
	}
	return outPath, nil
}

// metaRowWithoutCWD re-emits a meta row with its CWD cleared and everything
// else untouched.
//
// It round-trips through SessionMeta rather than editing the JSON, so a field
// added to the struct travels without anyone remembering this function exists —
// which is the failure mode that produced the bug it fixes. The cost is that a
// field NOT on the struct is dropped; that is already true of every meta row
// terva writes, since the same struct marshals them.
func metaRowWithoutCWD(line []byte) ([]byte, error) {
	var row sessionLine
	if err := json.Unmarshal(line, &row); err != nil || row.Meta == nil {
		if err == nil {
			err = errors.New("meta row carries no meta object")
		}
		return nil, err
	}
	row.Meta.CWD = ""
	// At is preserved: the timeline's own ordering information is part of what
	// makes a later row meaningful.
	return json.Marshal(row)
}

// ImportSession copies the .tervasession file at srcPath into the
// running user's session store under the given root+cwd, rewriting
// the meta's id / cwd / started fields so the imported session is
// owned by the current user / directory / clock. Returns the path
// of the created session file, ready to pass to OpenSession.
//
// The imported session is a first-class terva session: it'll show up
// in /sessions, /jump, and on-disk summaries just like any other.
// Messages and usage rows are preserved verbatim.
func ImportSession(srcPath, root, cwd, version string) (string, error) {
	if srcPath == "" {
		return "", errors.New("import: source path is empty")
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("import: open source: %w", err)
	}
	defer src.Close()

	// Validate the file header before committing to a destination.
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 20*1024*1024)
	if !sc.Scan() {
		return "", errors.New("import: session file is empty")
	}
	var head sessionLine
	if err := json.Unmarshal(sc.Bytes(), &head); err != nil {
		return "", fmt.Errorf("import: parse meta: %w", err)
	}
	if head.Type != "meta" || head.Meta == nil {
		return "", errors.New("import: first line is not a meta row")
	}

	// Build the destination inside the current cwd's session dir
	// with a fresh timestamped name.
	dir := SessionsDir(root, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	newID := uuid.NewString()
	name := fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102-150405"), newID[:8])
	outPath := filepath.Join(dir, name)
	dst, err := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("import: create dst: %w", err)
	}
	defer dst.Close()
	bw := bufio.NewWriter(dst)

	// Write a fresh meta row claiming ownership.
	importMeta := SessionMeta{
		ID:       newID,
		CWD:      cwd,
		Model:    head.Meta.Model,
		Provider: head.Meta.Provider,
		Started:  time.Now().UTC(),
		Version:  version,
	}
	metaLine, err := json.Marshal(sessionLine{Type: "meta", Meta: &importMeta})
	if err != nil {
		return "", fmt.Errorf("import: marshal meta: %w", err)
	}
	if _, err := bw.Write(metaLine); err != nil {
		return "", err
	}
	if err := bw.WriteByte('\n'); err != nil {
		return "", err
	}

	// Rewind and stream the rest. Avoid bufio.Scanner so exported sessions with
	// huge JSONL rows import cleanly.
	//
	// Meta rows are replayed rather than dropped, because everything a session
	// IS beyond its birth — mode, card, cast, greeting, lorebook, note,
	// background, bound user, and each model switch — is written in a row after
	// the first. Dropping them imported a Stage session as a plain coding one.
	//
	// They are replayed with this import's IDENTITY forced onto each, which is
	// the part that cannot be inherited: the source's id names a session that
	// does not exist here, its cwd is another machine's, its start time and
	// version describe the export rather than this copy, and its Parent points
	// at a branch that was not imported. Everything else is the state being
	// preserved and passes through untouched — so the model/provider timeline
	// still lines up with the messages interleaved between the rows.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("import: rewind: %w", err)
	}
	if err := forEachJSONLLine(src, func(line []byte) error {
		var h sessionLineHead
		if err := json.Unmarshal(line, &h); err != nil {
			return nil
		}
		out := line
		if h.Type == "meta" {
			var err error
			if out, err = importedMetaRow(line, importMeta); err != nil {
				return nil // unreadable row: keep the import, lose that step
			}
		}
		if _, err := bw.Write(out); err != nil {
			return err
		}
		return bw.WriteByte('\n')
	}); err != nil {
		return "", fmt.Errorf("import: read source: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return "", err
	}
	return outPath, nil
}

// importedMetaRow re-emits a source meta row under the importing session's
// identity, keeping every field that describes the session's STATE.
//
// owner supplies the four fields that are this copy's rather than the
// original's, plus Parent/ForkPoint, which are cleared: the source's parent
// names a branch that was not imported, and a dangling parent id makes the
// tree picker render a session hanging off nothing.
//
// Written as a whitelist of what to overwrite rather than a whitelist of what
// to keep, deliberately: a new SessionMeta field is state by default and
// travels on its own, which is the opposite of the arrangement that lost
// fourteen of them.
func importedMetaRow(line []byte, owner SessionMeta) ([]byte, error) {
	var row sessionLine
	if err := json.Unmarshal(line, &row); err != nil {
		return nil, err
	}
	if row.Meta == nil {
		return nil, errors.New("meta row carries no meta object")
	}
	row.Meta.ID = owner.ID
	row.Meta.CWD = owner.CWD
	row.Meta.Started = owner.Started
	row.Meta.Version = owner.Version
	row.Meta.Parent = ""
	row.Meta.ForkPoint = 0
	return json.Marshal(row)
}

// BranchSession creates a new session in root/cwd that contains the
// parent's messages 0..upToMessageIdx-1 (i.e. the first N user+
// assistant+tool rows). The new meta records Parent=<parent id> and
// ForkPoint=N so /session tree can rebuild the branch topology
// later. All non-message rows (usage) are preserved up to the cut
// point so the running cost tracker stays accurate.
//
// upToMessageIdx is a count over the flat message stream as
// returned by OpenSession. To "branch at user turn 3" the caller
// passes the index of that user message in msgs + 1 (so the
// message itself is included). The caller figures that out; this
// helper just copies the first N message rows.
//
// Returns the path of the new session file, ready for OpenSession.
func BranchSession(parentPath, root, cwd, version string, upToMessageIdx int) (string, error) {
	if parentPath == "" {
		return "", errors.New("branch: parent path is empty")
	}
	if upToMessageIdx < 0 {
		return "", errors.New("branch: upToMessageIdx must be >= 0")
	}

	src, err := os.Open(parentPath)
	if err != nil {
		return "", fmt.Errorf("branch: open parent: %w", err)
	}
	defer src.Close()

	// Read the parent's EFFECTIVE state so the child continues the same session
	// rather than the workspace default. Meta rows are an append-only timeline
	// whose LAST entry wins — every mutable setting is stamped by a row written
	// after NewSession's — so the first line alone would miss all of it. Scan the
	// file and keep the last meta, folding the lorebook across both of its
	// storage forms as the loader does.
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 20*1024*1024)
	var parentMeta SessionMeta
	var parentLore []WorldLoreEntry
	haveMeta := false
	for sc.Scan() {
		var head sessionLine
		if err := json.Unmarshal(sc.Bytes(), &head); err != nil {
			continue
		}
		switch {
		case head.Type == "meta" && head.Meta != nil:
			parentMeta = *head.Meta
			parentLore = foldMetaLore(parentLore, *head.Meta)
			haveMeta = true
		case head.Type == recordLore && head.Lore != nil:
			parentLore = applyLoreOp(parentLore, *head.Lore)
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("branch: scan parent: %w", err)
	}
	if !haveMeta {
		return "", errors.New("branch: parent has no meta row")
	}

	// Build the destination file.
	dir := SessionsDir(root, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	newID := uuid.NewString()
	name := fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102-150405"), newID[:8])
	outPath := filepath.Join(dir, name)
	dst, err := os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("branch: create dst: %w", err)
	}
	defer dst.Close()
	bw := bufio.NewWriter(dst)

	// Write the branch meta. The child inherits the parent's whole state — a
	// forked Stage session must continue as the SAME session, which means not
	// just its creation spec but its author's note, backdrop, bound user persona,
	// coordination, World, cast-model pins and lorebook.
	//
	// Written as a whitelist of what to OVERWRITE rather than one of what to
	// keep. The old shape listed five fields and silently dropped the other nine
	// as they were added over time, which is the same arrangement that lost
	// fourteen fields on import (see importedMetaRow): a new SessionMeta field
	// must be state that travels by default, or the next one added is lost the
	// day it ships and nobody finds out until a fork looks wrong.
	branchMeta := parentMeta
	branchMeta.ID = newID
	branchMeta.CWD = cwd
	branchMeta.Started = time.Now().UTC()
	branchMeta.Version = version
	branchMeta.Parent = parentMeta.ID
	branchMeta.ForkPoint = upToMessageIdx
	// The child re-serializes its prefix as typed message rows and copies no
	// amends, so it declares the base format version — raised below only if it
	// actually carries a lore row.
	branchMeta.FormatVersion = sessionFormatVersion
	// A title is a name for a conversation, and the branch is a different one.
	// Inheriting it would also mark the child's title as user-chosen (a meta-row
	// title reads as manual), which is what blocks automatic re-titling — so the
	// fork would keep a name describing the scene it diverged from, forever.
	branchMeta.Title = ""
	// The lorebook travels as its own row, below.
	branchMeta.WorldLore = nil
	if len(parentLore) > 0 {
		branchMeta.FormatVersion = sessionFormatVersionLore
	}
	metaLine, err := json.Marshal(sessionLine{Type: "meta", Meta: &branchMeta})
	if err != nil {
		return "", fmt.Errorf("branch: marshal meta: %w", err)
	}
	if _, err := bw.Write(metaLine); err != nil {
		return "", err
	}
	if err := bw.WriteByte('\n'); err != nil {
		return "", err
	}
	// One set row establishes the inherited book. The parent's incremental
	// history is its own; the child starts from the state, not the story of how
	// it got there.
	if len(parentLore) > 0 {
		loreLine, err := json.Marshal(sessionLine{Type: recordLore, Lore: &sessionLore{Op: LoreOpSet, Entries: parentLore}})
		if err != nil {
			return "", fmt.Errorf("branch: marshal lore: %w", err)
		}
		if _, err := bw.Write(loreLine); err != nil {
			return "", err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return "", err
		}
	}

	// Reconstruct the effective transcript the same way OpenSession does:
	// message rows append, and a compaction row replaces everything before
	// it. The fork index (upToMessageIdx) is defined over that effective
	// stream, not over the raw audit rows kept on disk before a compaction.
	// Without this, a fork taken after a compaction copies stale
	// pre-compaction rows and mis-maps the cut point.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("branch: rewind parent: %w", err)
	}
	rep := &loadReport{}
	var nonCompactedRows [][]byte
	sawCompaction := false
	sawAmend := false
	// walkSession reconstructs the effective transcript (append a message, reset
	// on a compaction, apply an amend); the hooks additionally keep the raw bytes
	// of the pre-compaction prefix rows so an uncompacted branch copies them
	// verbatim. onMessage's idx and onUsage's effLen are the effective length at
	// that row, matching the pre-append counter the verbatim-copy cutoff keys on.
	effective, walkErr := walkSession(src, rep, sessionWalkHooks{
		onMessage: func(_ provider.Message, idx int, line []byte) {
			if !sawCompaction && idx < upToMessageIdx {
				nonCompactedRows = append(nonCompactedRows, append([]byte(nil), line...))
			}
		},
		onUsage: func(_, _ provider.Usage, effLen int, _ bool, line []byte) {
			if !sawCompaction && effLen < upToMessageIdx {
				nonCompactedRows = append(nonCompactedRows, append([]byte(nil), line...))
			}
		},
		onCompaction: func(_, _ []provider.Message, _ int, _ []byte) {
			sawCompaction = true
		},
		onAmend: func(_ string, _ int, _ []byte) {
			sawAmend = true
		},
	})
	if walkErr != nil && walkErr != io.EOF {
		return "", fmt.Errorf("branch: read parent: %w", walkErr)
	}
	if sawCompaction || sawAmend {
		// The parent compacted or was amended: the effective transcript no
		// longer maps onto raw on-disk rows, so re-serialize the cut prefix as
		// fresh message rows the branch can replay directly.
		limit := upToMessageIdx
		if limit > len(effective) {
			limit = len(effective)
		}
		for i := 0; i < limit; i++ {
			w := encodeWireMessage(effective[i])
			line, err := json.Marshal(sessionLine{Type: "message", Message: &w})
			if err != nil {
				return "", fmt.Errorf("branch: marshal message: %w", err)
			}
			if _, err := bw.Write(line); err != nil {
				return "", err
			}
			if err := bw.WriteByte('\n'); err != nil {
				return "", err
			}
		}
	} else {
		// No compaction: copy the original message/usage rows verbatim so
		// the branch prefix is byte-identical to the parent.
		for _, row := range nonCompactedRows {
			if _, err := bw.Write(row); err != nil {
				return "", err
			}
			if err := bw.WriteByte('\n'); err != nil {
				return "", err
			}
		}
	}
	if err := bw.Flush(); err != nil {
		return "", err
	}
	return outPath, nil
}

// TreeNode is one entry in the branch tree returned by
// BuildSessionTree. Children are populated by linking on Parent ID.
type TreeNode struct {
	Summary  SessionSummary
	Meta     SessionMeta
	Children []*TreeNode
}

// BuildSessionTree loads every session in the cwd dir and returns
// the forest rooted at parentless sessions, with each non-root
// session placed under its parent. Used by /session tree to render
// the branch hierarchy.
func BuildSessionTree(root, cwd string) []*TreeNode {
	dir := SessionsDir(root, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	nodes := make(map[string]*TreeNode)
	order := []string{}
	for _, e := range entries {
		if e.IsDir() || !isSessionTranscriptName(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		summary := describeSession(path)
		meta, _ := readSessionMeta(path)
		if meta.ID == "" {
			continue
		}
		nodes[meta.ID] = &TreeNode{Summary: summary, Meta: meta}
		order = append(order, meta.ID)
	}
	var roots []*TreeNode
	for _, id := range order {
		n := nodes[id]
		if n.Meta.Parent == "" {
			roots = append(roots, n)
			continue
		}
		if parent, ok := nodes[n.Meta.Parent]; ok {
			parent.Children = append(parent.Children, n)
		} else {
			// Parent file missing (was manually deleted). Treat as
			// a root so it still shows up in the tree.
			roots = append(roots, n)
		}
	}
	return roots
}

// readSessionMeta opens path, reads the meta row, and returns it.
// Empty SessionMeta when the file is missing or not a valid session.
func readSessionMeta(path string) (SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 20*1024*1024)
	if !sc.Scan() {
		return SessionMeta{}, errors.New("empty file")
	}
	var line sessionLine
	if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
		return SessionMeta{}, err
	}
	if line.Type != "meta" || line.Meta == nil {
		return SessionMeta{}, errors.New("first line is not meta")
	}
	return *line.Meta, nil
}

// FindSessionByID looks up a session file in root/cwd whose meta id
// matches. Used by /session tree when the user picks an entry. O(n)
// over the files in the dir; the list is small in practice.
func FindSessionByID(root, cwd, id string) string {
	dir := SessionsDir(root, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !isSessionTranscriptName(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		meta, err := readSessionMeta(path)
		if err != nil {
			continue
		}
		if meta.ID == id {
			return path
		}
	}
	return ""
}

// firstUserPrompt scans forward from the current source position
// looking for the first user-role message and returns its text.
// Used to build a humane export filename. Uses Reader instead of
// Scanner so a very large JSONL row before the first user prompt
// cannot trip Scanner's token limit.
func firstUserPrompt(src io.Reader) (string, error) {
	r := bufio.NewReader(src)
	for {
		lineBytes, err := r.ReadBytes('\n')
		if len(lineBytes) > 0 {
			lineBytes = bytes.TrimRight(lineBytes, "\r\n")
			var line sessionLine
			if err := json.Unmarshal(lineBytes, &line); err == nil {
				if line.Type == "message" && line.Message != nil && line.Message.Role == "user" {
					b, _ := json.Marshal(line.Message)
					var m struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					}
					_ = json.Unmarshal(b, &m)
					for _, c := range m.Content {
						if c.Text != "" {
							return c.Text, nil
						}
					}
				}
			}
		}
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", err
		}
	}
}

// filenameFor builds a descriptive .tervasession filename from the
// session's start time and, when available, an excerpt of the
// first user prompt.
func filenameFor(started time.Time, id, firstPrompt string) string {
	base := started.UTC().Format("20060102-150405")
	if id != "" && len(id) >= 8 {
		base += "-" + id[:8]
	}
	slug := slugify(firstPrompt, 40)
	if slug != "" {
		base += "-" + slug
	}
	return base + PortableExt
}

// slugify lowercases, strips punctuation, collapses whitespace to
// hyphens, and truncates to max runes so it's safe as a filename.
func slugify(s string, max int) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	var out strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && out.Len() > 0 {
				out.WriteByte('-')
				prevDash = true
			}
		}
		if out.Len() >= max {
			break
		}
	}
	return strings.TrimRight(out.String(), "-")
}
