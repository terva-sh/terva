package workspace

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// World lore on the wire (Worlds L1). world.lore.put / world.lore.delete write
// SessionMeta.WorldLore (a durable, last-wins meta row) AND update the live
// build.WorldLoreRecord the per-turn tail scans, then broadcast a snapshot so
// the steering drawer re-renders. The two writes keep the persisted value and
// the live-injected value in step; an edit takes effect on the next turn with
// no cache bust. Optional controller, like NoteController.
var _ ctrlproto.WorldController = (*Workspace)(nil)

// WorldLorePut adds or updates one World lore entry (upsert by name; a rename
// passes the superseded name as p.Replace). Only immersive sessions carry a
// World; a coding session is a bad request.
func (w *Workspace) WorldLorePut(_ context.Context, sess string, p ctrlproto.WorldLorePutParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if s.worldLore == nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("World lore is only available for chat/play sessions"))
	}
	entry, err := worldLoreFromWire(p.Entry)
	if err != nil {
		return err
	}
	replace := strings.TrimSpace(p.Replace)
	if replace == "" {
		replace = entry.Name
	}
	next := make([]core.WorldLoreEntry, 0, len(s.sess.Meta.WorldLore)+1)
	placed := false
	for _, e := range s.sess.Meta.WorldLore {
		// The scene-state pin (SD4) matches its slot case-insensitively: there
		// is one card, however an import spelled it, and a put addressed to the
		// pin must update that card, never stand a second one beside it.
		samePin := core.IsSceneState(entry.Name) && core.IsSceneState(e.Name)
		switch {
		case e.Name == replace || samePin:
			if placed {
				continue // a rename onto the pin collapses both slots into one card
			}
			// The superseded slot keeps its position — an edit doesn't shuffle
			// the list. The learned-when ledger survives a user edit (it is
			// provenance, not content); the Model flag does not — an edit takes
			// ownership.
			entry.Learned = e.Learned
			next = append(next, entry)
			placed = true
		case e.Name == entry.Name:
			// A rename onto an existing entry's name would leave two entries
			// answering to one name; the later put/delete verbs key on names,
			// so refuse rather than silently shadow.
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a World lore entry named %q already exists", entry.Name))
		default:
			next = append(next, e)
		}
	}
	if !placed {
		next = append(next, entry)
	}
	return s.setWorldLore(next)
}

// WorldLoreDelete removes the World lore entry named p.Name.
func (w *Workspace) WorldLoreDelete(_ context.Context, sess string, p ctrlproto.WorldLoreDeleteParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if s.worldLore == nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("World lore is only available for chat/play sessions"))
	}
	name := strings.TrimSpace(p.Name)
	next := make([]core.WorldLoreEntry, 0, len(s.sess.Meta.WorldLore))
	for _, e := range s.sess.Meta.WorldLore {
		if e.Name != name {
			next = append(next, e)
		}
	}
	if len(next) == len(s.sess.Meta.WorldLore) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no World lore entry named %q", name))
	}
	if len(next) == 0 {
		next = nil
	}
	return s.setWorldLore(next)
}

// WorldSet updates the World's settings — today, the meta-narrator
// coordination mode (W3). Validated against the roster so a focus target is
// always a real character; takes effect on the next turn (the route decision
// reads meta fresh each prompt).
func (w *Workspace) WorldSet(_ context.Context, sess string, p ctrlproto.WorldSetParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if s.worldLore == nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the World is only available for chat/play sessions"))
	}
	mode := strings.TrimSpace(p.Coordination)
	switch {
	case mode == "" || mode == CoordinationOff:
		// valid
	case strings.HasPrefix(mode, coordinationFocus):
		name := strings.TrimSpace(strings.TrimPrefix(mode, coordinationFocus))
		if _, ok := s.castRefs()[name]; !ok {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no roster character %q to focus on", name))
		}
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("coordination must be \"\", %q, or %q<name>", CoordinationOff, coordinationFocus))
	}
	if err := s.sess.SetCoordination(mode); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "set coordination: %v", err)
	}
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// WorldsList lists the saved Worlds with member-session counts (W5).
func (w *Workspace) WorldsList(ctx context.Context) (ctrlproto.WorldsListResult, error) {
	docs, err := build.NewWorldStore().List()
	if err != nil {
		return ctrlproto.WorldsListResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "list worlds: %v", err)
	}
	counts := map[string]int{}
	if sessions, err := w.Sessions(ctx); err == nil {
		for _, si := range sessions {
			if si.World != "" {
				counts[si.World]++
			}
		}
	}
	out := ctrlproto.WorldsListResult{Worlds: make([]ctrlproto.WorldView, 0, len(docs))}
	for _, d := range docs {
		out.Worlds = append(out.Worlds, worldDocToView(d, counts[d.ID]))
	}
	return out, nil
}

// WorldSave promotes or updates (W5): the session's live World state — roster,
// pins, lore, coordination — lifted into the saved library. First save mints
// the World (p.Name required) and stamps membership; a member session updates
// its World in place, last-wins (p.Name renames). Explicit save-back only.
func (w *Workspace) WorldSave(_ context.Context, sess string, p ctrlproto.WorldSaveParams) (ctrlproto.WorldView, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	saved, err := s.saveWorld(p.Name, p.Description)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return worldDocToView(saved, 0), nil
}

// saveWorld lifts s's live World state into the saved library and stamps s into
// the result — the shared half of worlds.save. Split out because the next scene
// (SD5) promotes on the author's behalf: a story whose scenes were never
// explicitly promoted had no World to put them in, so the successor inherited an
// empty membership and the two scenes ended up with nothing grouping them.
//
// Callers own the broadcast: worlds.save is a direct request and answers with a
// snapshot, while the next-scene commit is mid-flight through a create and
// broadcasts once at the end.
func (s *wsSession) saveWorld(name, description string) (build.WorldDoc, error) {
	if s.worldLore == nil {
		return build.WorldDoc{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the World is only available for chat/play sessions"))
	}
	p := ctrlproto.WorldSaveParams{Name: name, Description: description}
	store := build.NewWorldStore()
	doc := build.WorldDoc{
		ID:              s.sess.Meta.World,
		Name:            strings.TrimSpace(p.Name),
		Description:     strings.TrimSpace(p.Description),
		Characters:      s.castRefs(),
		CharacterModels: s.castModels(),
		Lore:            s.sess.Meta.WorldLore,
		Coordination:    s.sess.Meta.Coordination,
	}
	// The World lifts the WHOLE stage: the bound character joins the saved
	// roster (the cast deliberately excludes them in-session), so the World
	// sheet lists every character and chat-in-World can bind any of them.
	if boundRef := strings.TrimSpace(s.sess.Meta.Card); boundRef != "" {
		if boundName, _ := s.boundCharacter(); boundName != "" && boundName != "the character" {
			if _, in := doc.Characters[boundName]; !in {
				doc.Characters[boundName] = boundRef
			}
		}
	}
	if doc.ID != "" {
		if prev, err := store.Get(doc.ID); err == nil {
			if doc.Name == "" {
				doc.Name = prev.Name
			}
			if doc.Description == "" {
				doc.Description = prev.Description
			}
		} else {
			doc.ID = "" // the saved World vanished from disk — re-promote fresh
		}
	}
	if doc.ID == "" && doc.Name == "" {
		return build.WorldDoc{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("name the World to save it"))
	}
	saved, err := store.Save(doc)
	if err != nil {
		return build.WorldDoc{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "save world: %v", err)
	}
	if s.sess.Meta.World != saved.ID {
		if err := s.sess.SetWorld(saved.ID); err != nil {
			return build.WorldDoc{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "stamp world membership: %v", err)
		}
	}
	return saved, nil
}

// WorldDelete removes a saved World; member sessions keep their embedded
// copies and lose only the grouping.
func (w *Workspace) WorldDelete(_ context.Context, p ctrlproto.WorldDeleteParams) error {
	if err := build.NewWorldStore().Delete(strings.TrimSpace(p.ID)); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	return nil
}

// WorldUpdate edits a saved World's metadata — name, description, cover —
// from the Library sheet, no session involved (worlds.save is the content
// path; this touches nothing a session wrote). The id never changes, so
// member-session grouping survives a rename.
func (w *Workspace) WorldUpdate(_ context.Context, p ctrlproto.WorldUpdateParams) (ctrlproto.WorldView, error) {
	if len(p.Cover) > 0 && p.RemoveCover {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("worlds.update cannot both set and remove the cover"))
	}
	store := build.NewWorldStore()
	doc, err := store.Get(strings.TrimSpace(p.ID))
	if err != nil {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		doc.Name = name
	}
	doc.Description = strings.TrimSpace(p.Description)
	saved, err := store.Save(doc)
	if err != nil {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "update world: %v", err)
	}
	if len(p.Cover) > 0 {
		if err := store.SetCover(saved.ID, p.Cover); err != nil {
			return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "set cover: %v", err)
		}
	} else if p.RemoveCover {
		if err := store.RemoveCover(saved.ID); err != nil {
			return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "remove cover: %v", err)
		}
	}
	return worldDocToView(saved, 0), nil
}

// WorldsExport bundles a saved World for download (W5b): the WorldDoc plus
// every roster character's card in its ordinary export form (a CCv2 PNG when
// it retains an avatar, else the CCv2 JSON), plus the cover image. A roster
// ref no longer in the card library is skipped — the bundle carries what the
// library still holds; the roster entry itself stays for the humans reading
// the file.
func (w *Workspace) WorldsExport(_ context.Context, p ctrlproto.WorldExportParams) (ctrlproto.WorldExport, error) {
	store := build.NewWorldStore()
	doc, err := store.Get(strings.TrimSpace(p.ID))
	if err != nil {
		return ctrlproto.WorldExport{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	bundle := build.WorldBundle{Spec: build.WorldBundleSpec, World: doc}
	seen := map[string]bool{}
	for _, name := range sortedNames(doc.Characters) {
		ref := doc.Characters[name]
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		sc, err := w.cardStore().Get(ref)
		if err != nil {
			continue
		}
		exp, err := w.exportCard(sc, "")
		if err != nil {
			continue
		}
		bundle.Cards = append(bundle.Cards, build.BundleCard{Ref: ref, Name: sc.Card.Name, Mime: exp.MimeType, Bytes: exp.Bytes})
	}
	if path := store.CoverPath(doc.ID); path != "" {
		if cover, err := os.ReadFile(path); err == nil {
			bundle.Cover = cover
		}
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return ctrlproto.WorldExport{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "encode bundle: %v", err)
	}
	return ctrlproto.WorldExport{Filename: exportFilename(doc.Name) + ".world.json", MimeType: "application/json", Bytes: raw}, nil
}

// WorldsImport ingests a World bundle: every embedded card lands in the card
// library (idempotent by content — a re-import dedupes), the roster is
// remapped from the EXPORTING library's ids to the ids this one assigned, and
// a fresh World id is always minted, so a bundle can never collide with or
// overwrite a local World. A roster entry whose card neither rode the bundle
// nor already exists locally is dropped rather than left dangling.
func (w *Workspace) WorldsImport(_ context.Context, p ctrlproto.WorldImportParams) (ctrlproto.WorldView, error) {
	data := p.Bytes
	if len(data) == 0 && strings.TrimSpace(p.Path) != "" {
		var err error
		if data, err = os.ReadFile(strings.TrimSpace(p.Path)); err != nil {
			return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "read bundle: %v", err)
		}
	}
	if len(data) == 0 {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("worlds.import needs bytes or a path"))
	}
	var bundle build.WorldBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not a World bundle: %v", err))
	}
	if bundle.Spec != build.WorldBundleSpec {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not a World bundle (spec %q)", bundle.Spec))
	}
	if strings.TrimSpace(bundle.World.Name) == "" {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the bundle's World has no name"))
	}
	remap := map[string]string{}
	imported := false
	for _, bc := range bundle.Cards {
		if len(bc.Bytes) == 0 {
			continue
		}
		sc, err := w.cardStore().ImportBytes(bc.Bytes)
		if err != nil {
			continue // one corrupt embedded card must not sink the World
		}
		imported = true
		if bc.Ref != "" {
			remap[bc.Ref] = sc.ID
		}
	}
	doc := bundle.World
	doc.ID = "" // the bundle's id is provenance, not identity — mint fresh
	characters := make(map[string]string, len(doc.Characters))
	for name, ref := range doc.Characters {
		if mapped, ok := remap[ref]; ok {
			characters[name] = mapped
		} else if _, err := w.cardStore().Get(ref); err == nil {
			characters[name] = ref // already in this library (a local re-import)
		}
	}
	doc.Characters = characters
	for name := range doc.CharacterModels {
		if _, ok := characters[name]; !ok {
			delete(doc.CharacterModels, name) // a pin without its member
		}
	}
	store := build.NewWorldStore()
	saved, err := store.Save(doc)
	if err != nil {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "import world: %v", err)
	}
	if len(bundle.Cover) > 0 {
		if err := store.SetCover(saved.ID, bundle.Cover); err != nil {
			return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "set cover: %v", err)
		}
	}
	if imported {
		w.broadcastLibraryChanged()
	}
	return worldDocToView(saved, 0), nil
}

func worldDocToView(d build.WorldDoc, sessions int) ctrlproto.WorldView {
	v := ctrlproto.WorldView{
		ID:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Characters:  d.Characters,
		Lore:        worldLoreToView(d.Lore),
		Sessions:    sessions,
		Created:     ctrlTimeString(d.Created),
		Updated:     ctrlTimeString(d.Updated),
	}
	if build.NewWorldStore().CoverPath(d.ID) != "" {
		v.CoverURL = "/media/worlds/" + d.ID
	}
	return v
}

// setWorldLore persists the new entry list (last-wins meta row), updates the
// live record the per-turn tail scans, and broadcasts the new snapshot. The
// record gets only what the tail's speaker may see (L2): the tail feeds the
// BOUND character's generations in a chat, so it is audience-filtered for
// them; a play session's tail feeds the director, who sees everything.
func (s *wsSession) setWorldLore(entries []core.WorldLoreEntry) error {
	entries = stampScenePin(entries, s.sess.Meta.WorldLore, s.messageCount())
	if err := s.sess.SetWorldLore(entries); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "set World lore: %v", err)
	}
	s.worldLore.Set(build.WorldLoreEntries(worldLoreFor(entries, s.tailLoreAudience())))
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// stampScenePin dates the scene-state pin (SD6) against the message count when
// its content changed. Every writer of the pin — the drawer form, world_note,
// an accepted doctor proposal — funnels through setWorldLore, so stamping here
// covers all of them and any future one for free.
//
// It stamps on CONTENT CHANGE, not on every write: adding an unrelated lore
// entry rewrites the whole list, and re-dating the pin then would make it read
// as fresh every time the author touched anything else — which is precisely the
// signal being measured. An unchanged pin carries its old stamp forward.
func stampScenePin(next, prev []core.WorldLoreEntry, msgs int) []core.WorldLoreEntry {
	was, had := "", 0
	for _, e := range prev {
		if core.IsSceneState(e.Name) {
			was, had = strings.TrimSpace(e.Content), e.PinnedAt
			break
		}
	}
	out := make([]core.WorldLoreEntry, len(next))
	copy(out, next)
	for i, e := range out {
		if !core.IsSceneState(e.Name) {
			continue
		}
		if strings.TrimSpace(e.Content) == was {
			out[i].PinnedAt = had // untouched by this write — keep its date
		} else {
			out[i].PinnedAt = msgs
		}
		break
	}
	return out
}

// scenePinDrift reports how much scene has played since the pin was last
// written, and whether there is a pin to be stale at all. A pin stamped ahead
// of the count (a hand-edited meta, a bundle import) clamps to 0 rather than
// reporting negative drift.
func scenePinDrift(entries []core.WorldLoreEntry, msgs int) (turns int, pinned bool) {
	for _, e := range entries {
		if !core.IsSceneState(e.Name) {
			continue
		}
		if n := msgs - e.PinnedAt; n > 0 {
			return n, true
		}
		return 0, true
	}
	return 0, false
}

// messageCount is the played length of the session, tolerating a session whose
// agent is not built yet — info() is rendered on paths (a title settle, a
// listing) that run before or without one.
func (s *wsSession) messageCount() int {
	if s.agent == nil {
		return 0
	}
	return len(s.agent.Messages())
}

// scenePinStaleFor is the snapshot's view of the drift: the turn count when
// there is a pin, 0 when there is none (the field is omitempty, so a session
// with no pin carries nothing rather than a "0 turns stale" that would read as
// a current card).
func scenePinStaleFor(entries []core.WorldLoreEntry, msgs int) int {
	turns, pinned := scenePinDrift(entries, msgs)
	if !pinned {
		return 0
	}
	return turns
}

// tailLoreAudience is who the session tail speaks for, for lore scoping: the
// bound character in a chat, the all-seeing director ("") in play.
func (s *wsSession) tailLoreAudience() string {
	if s.sess.Meta.Experience != "chat" {
		return ""
	}
	name, _ := s.boundCharacter()
	return name
}

// worldLoreFor returns the entries `name` is cleared for: world-shared entries
// (no audience) plus those whose audience names them. The empty name is the
// scene authority — the narrator, the play director, the router — and sees
// everything.
func worldLoreFor(entries []core.WorldLoreEntry, name string) []core.WorldLoreEntry {
	if name == "" {
		return entries
	}
	var out []core.WorldLoreEntry
	for _, e := range entries {
		if len(e.Audience) == 0 || audienceHas(e.Audience, name) {
			out = append(out, e)
		}
	}
	return out
}

// audienceHas reports whether an audience names a character (whitespace- and
// case-forgiving: names are typed by people).
func audienceHas(audience []string, name string) bool {
	for _, a := range audience {
		if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// worldLoreFromWire validates one wire entry and maps it to the persisted
// form. The engine's activation rule is the contract (see lore.ParseEntry): an
// entry needs content, and needs keys unless it is constant — otherwise it
// could never fire.
func worldLoreFromWire(e ctrlproto.WorldLoreEntry) (core.WorldLoreEntry, error) {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return core.WorldLoreEntry{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a World lore entry needs a name"))
	}
	content := strings.TrimSpace(e.Content)
	if content == "" {
		return core.WorldLoreEntry{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a World lore entry needs content"))
	}
	// The pinned scene-state card (SD4) is always-on and shared BY DEFINITION —
	// a keyed or secret state card is a contradiction (state that only sometimes
	// applies isn't state; a clock some characters can't see isn't the scene's
	// clock). Normalize rather than reject, so every writer — the drawer form,
	// the doctor's accept, a hand-typed put — lands the same canonical pin.
	if core.IsSceneState(name) {
		return core.WorldLoreEntry{Name: core.SceneStateName, Constant: true, Content: content}, nil
	}
	var keys []string
	for _, k := range e.Keys {
		if t := strings.TrimSpace(k); t != "" {
			keys = append(keys, t)
		}
	}
	if len(keys) == 0 && !e.Constant {
		return core.WorldLoreEntry{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a World lore entry needs trigger keywords, or mark it always-on"))
	}
	if e.Constant {
		keys = nil
	}
	// The audience (L2): trimmed, deduped, order kept. Unknown names are
	// allowed — a character can be named before they are kept on stage; the
	// entry simply reaches nobody until they are.
	var audience []string
	for _, a := range e.Audience {
		t := strings.TrimSpace(a)
		if t == "" || audienceHas(audience, t) {
			continue
		}
		audience = append(audience, t)
	}
	return core.WorldLoreEntry{Name: name, Keys: keys, Constant: e.Constant, Content: content, Audience: audience}, nil
}

// worldLoreToView maps persisted World lore onto the wire view for SessionInfo.
func worldLoreToView(entries []core.WorldLoreEntry) []ctrlproto.WorldLoreEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]ctrlproto.WorldLoreEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, ctrlproto.WorldLoreEntry{Name: e.Name, Keys: e.Keys, Constant: e.Constant, Content: e.Content, Audience: e.Audience, Model: e.Model, Learned: e.Learned})
	}
	return out
}
