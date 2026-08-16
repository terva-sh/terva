package modes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// The two ways the /shared panel reaches outside terva, behind vars so a test
// can watch them instead of running them.
//
// Neither success path could be tested otherwise: one shells out to xclip or
// wl-copy, the other launches whatever the desktop opens a PNG with. A suite
// that cannot let those run can only ever assert the REFUSALS — and it did,
// which is how `if !i.cfg.CarrierLocal` could be inverted to `if true` (copy
// and open always refuse, on every carrier) with the whole package still
// green. The feature's main path was the untested one.
//
// Vars rather than an interface on Interactive: there is one implementation and
// one test seam, and the surrounding code is a host method calling a package
// function. A field would spread the seam through every construction site.
var (
	writeClipboard   = tui.WriteClipboardText
	openSystemViewer = openInSystemViewer
)

// openInSystemViewer hands a path to the platform's "open this with whatever
// handles it" command — the same three-way switch the OAuth browser launch
// uses (packages/provider/auth/manager.go).
//
// Start rather than Run: the viewer is a separate application and may well
// outlive terva, so waiting on it would hang the TUI for as long as the user
// keeps the file open.
func openInSystemViewer(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// Shared-file previews for the transcript cards.
//
// A share on the wire is a HANDLE, not bytes: the record carries an id, a name,
// and a size, and the file itself lives in the daemon's store. The web panel
// resolves that handle over an HTTP route it mounts; the TUI has no such route,
// so it asks for the bytes over the control plane (shared.fetch) and hands them
// to the renderer, which draws them with the same inline-image path a tool
// result's images use.
//
// Only images are fetched. Everything else is a card and a path — there is
// nothing a terminal can do with a PDF's bytes that the path does not do
// better, and pulling a 200 MB video through the control plane to render
// nothing would be a cost with no product.

// sharedCarrier is the optional slice of the carrier this surface needs.
//
// Type-asserted exactly as workflowsCarrier asserts its own: SharedFilesController
// is optional, so a replay carrier — which has no store behind it — simply does
// not implement it, and every caller here degrades to "no preview" or "not
// available" rather than to an error.
func (i *Interactive) sharedCarrier() (ctrlproto.SharedFilesController, bool) {
	if i.cfg.Carrier == nil {
		return nil, false
	}
	sc, ok := i.cfg.Carrier.(ctrlproto.SharedFilesController)
	return sc, ok
}

// maxPreviewBytes bounds what the TUI will pull for a PREVIEW, well under the
// wire's own MaxSharedFetchBytes.
//
// The two bounds answer different questions. The wire's asks "can this ride a
// control frame at all"; this one asks "is it worth pulling to draw a picture
// twenty rows tall". An image past this is still listed, still has a path, and
// still opens in a real viewer — it just does not become a thumbnail nobody
// asked to wait for.
const maxPreviewBytes = 2 << 20

// fetchSharedPreviews pulls the bytes for any image shares on screen that this
// client has not asked about yet.
//
// Called from the render path, which must never block, so the fetch runs on its
// own goroutine and repaints when it lands. The `fetched` set is marked BEFORE
// the request goes out, under the same lock that reads it: a card is on screen
// every frame, and without that claim each repaint would launch another request
// for a file already in flight.
func (i *Interactive) fetchSharedPreviews(files []core.SharedFile) {
	i.fetchSharedPreviewsFor(i.carrierSession(), files)
}

// fetchSharedPreviewsFor is fetchSharedPreviews for a session id the caller
// already holds.
//
// Split out so the stale-binding path is testable without racing a real switch:
// the id is read outside the lock, so a test must be able to hand in one that
// has since been superseded and see it refused.
func (i *Interactive) fetchSharedPreviewsFor(sess string, files []core.SharedFile) {
	sc, ok := i.sharedCarrier()
	if !ok || len(files) == 0 {
		return
	}
	var want []string

	i.mu.Lock()
	if sess != i.cfg.CarrierSession {
		// A switch landed between reading the session and taking the lock, so
		// this call speaks for a binding that is no longer current.
		i.mu.Unlock()
		return
	}
	if i.sharedPreviewsSession != sess {
		// Adopt the binding on FIRST USE, having just confirmed under this
		// lock that sess is the session the TUI is actually bound to.
		//
		// Only SwitchCarrierSession used to stamp this field, but the three
		// construction paths (attach, ctrlproto, replay) build an Interactive
		// that is already bound and never switch at all. So on the ordinary
		// startup path the cache stayed unarmed and every fetch returned here
		// — previews silently never loaded until the user changed sessions,
		// which is the one thing most sessions never do.
		//
		// Dropping any bytes found here keeps the invariant the switch path
		// establishes: these maps only ever hold ids minted by the session
		// named in sharedPreviewsSession. The sibling caches (task board,
		// memory, and the shared LISTING below) all self-heal exactly here,
		// for the same reason.
		i.sharedPreviews = nil
		i.sharedPreviewsFetched = nil
		i.sharedPreviewsSession = sess
	}
	if i.sharedPreviewsFetched == nil {
		i.sharedPreviewsFetched = map[string]bool{}
	}
	for _, f := range files {
		if f.Kind != "image" || f.ID == "" {
			continue
		}
		if f.Size > maxPreviewBytes || i.sharedPreviewsFetched[f.ID] {
			continue
		}
		i.sharedPreviewsFetched[f.ID] = true
		want = append(want, f.ID)
	}
	i.mu.Unlock()

	if len(want) == 0 {
		return
	}
	go func() {
		for _, id := range want {
			content, err := sc.SharedFileFetch(context.Background(), sess, ctrlproto.SharedFileRef{ID: id})
			if err != nil || len(content.Data) == 0 {
				// A swept file, a daemon that does not serve the verb, or a
				// carrier that cannot. The card renders without a preview,
				// which is a complete card — and `fetched` stays set, so this
				// failure costs one request rather than one per frame.
				continue
			}
			i.mu.Lock()
			if i.sharedPreviewsSession == sess {
				if i.sharedPreviews == nil {
					i.sharedPreviews = map[string][]byte{}
				}
				i.sharedPreviews[id] = content.Data
			}
			i.mu.Unlock()
			i.invalidate()
		}
	}()
}

// transcriptShares collects the share records the rendered transcript carries.
//
// Read from the MESSAGES rather than from a shared.list call, because these are
// the shares that have a card on screen — which is exactly the set worth
// spending a fetch on. The listing verb answers a different question ("what is
// in the store"), and the drawer is what asks it.
func transcriptShares(msgs []provider.Message) []core.SharedFile {
	var out []core.SharedFile
	for _, m := range msgs {
		raw := m.Meta[core.MetaShared]
		if raw == "" {
			continue
		}
		var files []core.SharedFile
		if err := json.Unmarshal([]byte(raw), &files); err != nil {
			// The renderer tolerates a malformed record by dropping the card;
			// dropping the fetch with it keeps the two agreeing.
			continue
		}
		out = append(out, files...)
	}
	return out
}

// --- the /shared panel ---

// refreshSharedFiles refetches the listing and repaints.
//
// Synchronous, unlike the preview fetch: it is driven by a key (opening the
// panel, or r inside it), and there is nothing to show until it answers. The
// call is a directory read on the daemon's disk.
func (i *Interactive) refreshSharedFiles() error {
	sc, ok := i.sharedCarrier()
	if !ok {
		return errSharedUnavailable
	}
	sess := i.carrierSession()
	files, err := sc.SharedFiles(context.Background(), sess)
	if err != nil {
		if sharedUnsupported(err) {
			// A daemon with no share store, or a replay carrier: the same
			// answer as a carrier that never implemented the interface, so
			// give the caller the same error. Otherwise opening /shared on a
			// replay session reports "shared files: not supported", which
			// reads as a fault rather than as a mode without the feature.
			return errSharedUnavailable
		}
		return err
	}
	i.mu.Lock()
	if i.cfg.CarrierSession == sess {
		i.sharedFiles = files
		i.sharedFilesSession = sess
	}
	i.mu.Unlock()
	i.invalidate()
	return nil
}

// errSharedUnavailable is a carrier that does not serve the verbs at all.
var errSharedUnavailable = errors.New("shared files are not available here")

// sharedUnsupported reports the capability answer — nothing here serves the
// verb — as opposed to a real failure, which deserves its message on screen.
// The same distinction /tasks and /workflows draw, so a broken carrier does not
// masquerade as an intentional limitation.
//
// A carrier can decline in two places and only one of them is a Go type
// assertion. In-process, a carrier that does not implement
// SharedFilesController fails i.sharedCarrier() and never makes a call. Over a
// socket EVERY carrier satisfies the interface, because the client side is one
// concrete type that forwards whatever it is asked; the refusal comes back as a
// CodeUnsupported frame from a daemon with no share store, or from a replay
// carrier that has no store to read. Both are the same answer to the user and
// this is what makes them one.
//
// CodeNotFound is deliberately NOT folded in, and that is the difference from
// the sibling panels. Their surfaces return NotFound for "switched off for this
// session", a capability answer. Here SharedFileFetch returns it for a file the
// sweeper already took, or an id from another session — a per-file failure that
// the user must see as a failure, because the panel is still listing a row that
// promises the file is there.
func sharedUnsupported(err error) bool {
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Code == ctrlproto.CodeUnsupported
}

// sharedFileRows is what the panel renders, read from the cache each frame.
//
// The slice is COPIED: the panel holds it for the length of a render while a
// refresh may replace it from another goroutine.
func (i *Interactive) sharedFileRows() []ctrlproto.SharedFileEntry {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sharedFilesSession != i.cfg.CarrierSession {
		return nil
	}
	return append([]ctrlproto.SharedFileEntry(nil), i.sharedFiles...)
}

// sharedFileByID finds a cached row.
func (i *Interactive) sharedFileByID(id string) (ctrlproto.SharedFileEntry, bool) {
	for _, f := range i.sharedFileRows() {
		if f.ID == id {
			return f, true
		}
	}
	return ctrlproto.SharedFileEntry{}, false
}

// openSharedDialog fetches the listing and shows the panel.
func (i *Interactive) openSharedDialog() {
	if err := i.refreshSharedFiles(); err != nil {
		if errors.Is(err, errSharedUnavailable) {
			i.setStatusErr(i18n.T("shared files are not available in this mode"))
			return
		}
		i.setStatusErr(i18n.T("shared files: %s", err.Error()))
		return
	}
	i.sharedDialog.Open(i.sharedFileRows)
	i.invalidate()
}

// copySharedPath puts the file's path on the clipboard.
//
// LOCAL CARRIERS ONLY. The path names the daemon's filesystem, and on `terva
// attach` that is another machine: pasting it into a terminal here would open
// nothing, or — worse — something else with the same name. A remote client is
// told to save a copy instead, which is the verb that actually moves bytes.
func (i *Interactive) copySharedPath(id string) {
	file, ok := i.sharedFileByID(id)
	if !ok {
		i.sharedDialog.Notice(i18n.T("that file is no longer listed"), true)
		i.invalidate()
		return
	}
	if !i.cfg.CarrierLocal || file.Path == "" {
		i.sharedDialog.Notice(i18n.T("the file is on the daemon's host, not this one — press s to save a copy here"), true)
		i.invalidate()
		return
	}
	if err := writeClipboard(file.Path); err != nil {
		i.sharedDialog.Notice(i18n.T("copy failed: %s", err.Error()), true)
		i.invalidate()
		return
	}
	i.sharedDialog.Notice(i18n.T("copied %s", file.Path), false)
	i.invalidate()
}

// openSharedFile hands the file to the system's default application.
//
// Local carriers only, for copySharedPath's reason: there is no sense in which
// this host can open a file that lives on another one.
func (i *Interactive) openSharedFile(id string) {
	file, ok := i.sharedFileByID(id)
	if !ok {
		i.sharedDialog.Notice(i18n.T("that file is no longer listed"), true)
		i.invalidate()
		return
	}
	if !i.cfg.CarrierLocal || file.Path == "" {
		i.sharedDialog.Notice(i18n.T("the file is on the daemon's host, not this one — press s to save a copy here"), true)
		i.invalidate()
		return
	}
	if err := openSystemViewer(file.Path); err != nil {
		i.sharedDialog.Notice(i18n.T("open failed: %s", err.Error()), true)
		i.invalidate()
		return
	}
	// The listing's name, interpolated into a notice line. copySharedPath
	// below reports file.Path instead, which the store's own id-plus-
	// sanitizeName construction already constrains.
	i.sharedDialog.Notice(i18n.T("opened %s", tui.SanitizeLabel(file.Name)), false)
	i.invalidate()
}

// saveSharedFile writes a copy into the working directory.
//
// This is the verb that works EVERYWHERE, because it moves the bytes rather
// than pointing at them: it fetches over the control plane and writes locally,
// so a remote client gets a real file instead of a path it cannot use.
//
// It never overwrites. A name that is taken gets a numbered suffix, because the
// alternative is destroying the user's own work to make room for a copy they
// asked for as a convenience.
func (i *Interactive) saveSharedFile(id string) {
	sc, ok := i.sharedCarrier()
	if !ok {
		i.sharedDialog.Notice(i18n.T("shared files are not available in this mode"), true)
		i.invalidate()
		return
	}
	content, err := sc.SharedFileFetch(context.Background(), i.carrierSession(), ctrlproto.SharedFileRef{ID: id})
	if err != nil {
		if sharedUnsupported(err) {
			// Nothing was attempted, so nothing failed: the daemon has no
			// share store to fetch from. "save failed" would send the user
			// looking at their disk for a permission or space problem that
			// does not exist.
			i.sharedDialog.Notice(i18n.T("shared files are not available in this mode"), true)
			i.invalidate()
			return
		}
		i.sharedDialog.Notice(i18n.T("save failed: %s", err.Error()), true)
		i.invalidate()
		return
	}
	name := sanitizeSavedName(content.Name)
	f, path, err := createUnique(filepath.Join(i.cfg.CWD, name))
	if err != nil {
		i.sharedDialog.Notice(i18n.T("save failed: %s", err.Error()), true)
		i.invalidate()
		return
	}
	_, werr := f.Write(content.Data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		// The file is ours — createUnique just minted it — so a failed write
		// leaves a truncated copy that nobody asked for. Take it back rather
		// than leave a plausible-looking short file beside the real one.
		_ = os.Remove(path)
		i.sharedDialog.Notice(i18n.T("save failed: %s", werr.Error()), true)
		i.invalidate()
		return
	}
	// sanitizeSavedName decides what lands on DISK and guards path traversal;
	// it lets an escape byte through, because a control character in a
	// filename is legal and refusing to save is worse than saving it. The
	// terminal is a different question, so the notice sanitizes separately.
	i.sharedDialog.Notice(i18n.T("saved %s", tui.SanitizeLabel(filepath.Base(path))), false)
	i.invalidate()
}

// sanitizeSavedName reduces a share's name to one safe filename in the working
// directory.
//
// The name came off the daemon's store, which sanitized it on the way in — but
// this writes to the USER's tree, so it is checked again here rather than
// trusted across a machine boundary. Only the base survives, and a name that
// reduces to nothing becomes a fixed fallback.
func sanitizeSavedName(name string) string {
	clean := filepath.Base(strings.TrimSpace(name))
	clean = strings.TrimLeft(clean, ".")
	if clean == "" || clean == "." || clean == ".." || strings.ContainsRune(clean, os.PathSeparator) {
		return "shared-file"
	}
	return clean
}

// createUnique creates path, or the first free "name-N.ext" beside it, and
// returns the open handle along with the name it settled on.
//
// O_EXCL, and the exclusivity is the whole point. A stat answers a question
// about the PAST: between "does this name exist" and "write it" a file can
// appear — the user's own work, or a second save of the same share — and an
// unconditional write then destroys it. Only the kernel can check and create as
// one indivisible act, so a never-overwrite promise is made there or it is not
// made at all. It also declines to follow a symlink sitting on the name, which
// a stat-then-write would have written straight through — including a DANGLING
// one, which a stat reports as free and a write then creates the target of,
// outside the directory the user was saving into.
//
// 0o600, not the 0o644 an editor would leave. These bytes came from the agent
// and may be an export, a dump, or a report built from something private; on a
// multi-user host the wider mode hands them to every local account. It is the
// mode privfs.FileMode names and the one the share store itself writes with, so
// the copy is no more readable than the original was.
//
// Bounded rather than looping forever: a directory holding a thousand copies of
// one name is a situation to report, not to keep searching.
func createUnique(path string) (*os.File, string, error) {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for n := 1; n < 1000; n++ {
		candidate := path
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privfs.FileMode)
		if err == nil {
			return f, candidate, nil
		}
		if !errors.Is(err, os.ErrExist) {
			// A real failure (no such directory, no permission). Reporting it
			// is better than trying 998 more names that will fail the same way.
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("%s already exists", filepath.Base(path))
}

// sharedPreviewBytes returns the preview cache for the render pass.
//
// It COPIES the map, and the copy is the whole point. The View holds what it is
// given for the length of a frame and reads it outside this lock, while the
// fetch goroutine keeps inserting — and a map read concurrent with a map write
// is a race in Go, whoever owns the values. Handing over the live map would put
// one in the render path.
//
// Only the map is copied, not the bytes: a share id names one set of bytes
// forever (the store mints a new id rather than overwriting), so an entry never
// changes once inserted and the slices are safe to share. A session with no
// shares — nearly every session — allocates nothing.
func (i *Interactive) sharedPreviewBytes() map[string][]byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.sharedPreviewsSession != i.cfg.CarrierSession || len(i.sharedPreviews) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(i.sharedPreviews))
	for id, data := range i.sharedPreviews {
		out[id] = data
	}
	return out
}
