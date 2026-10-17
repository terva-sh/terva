package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
)

// The Talkoot team manager (TKT-01M4YR7T8, docs/proposals/talkoot-team-manager.md):
// one native session outside every talkoot. It reads every team on the daemon
// and proposes changes to any of them, and a person decides each proposal.
//
// 🔑 The mark lives in the session's meta (Meta.Manager), as a recruiter's
// binding does. A file under the talkoot directory names the manager of each
// workspace, so every client and every restart opens the same conversation.
// The file keys each session by the workspace directory, because a session
// store belongs to one workspace and a talkoot runs only in its home
// workspace. One record for the whole home would make each workspace replace
// the other's manager.

// managerFile names the manager session of each workspace, under
// talkoot.Dir().
const managerFile = "manager.json"

// managerGreeting is the manager's first message. It is static text, so
// opening the manager makes no model call.
const managerGreeting = "I manage the teams on this daemon. Tell me the team you want, and I draft it for you to review and create: for example a personal assistant, a coding team, a research team, or a decision council. Or name a team, and tell me what to change in it: a member to add, edit, or remove, a new look, or the team's title, charter, or daily budget. I propose each change, and you approve it."

// managerGreetingSource tags the greeting, so a reader can tell it from a
// model turn.
const managerGreetingSource = "talkoot:manager-greeting"

// managerMu keeps two first opens from making two managers.
var managerMu sync.Mutex

// managerRecord maps a workspace directory to its manager session.
type managerRecord struct {
	Sessions map[string]string `json:"sessions"`
}

// ManagerTalkoot returns the team manager session, and makes it the first
// time. A record that names a session which is gone, or which is not a
// manager, is replaced.
func (w *Workspace) ManagerTalkoot(ctx context.Context) (ctrlproto.SessionInfo, error) {
	managerMu.Lock()
	defer managerMu.Unlock()
	path := filepath.Join(talkoot.Dir(), managerFile)
	rec := readManagerRecord(path)
	if id := rec.Sessions[w.cwd]; id != "" && w.isManagerSession(id) {
		return w.ResumeSession(ctx, id)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Manager: true, Persona: RecruiterPersona, Title: "Team manager"})
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	rec.Sessions[w.cwd] = info.ID
	raw, _ := json.Marshal(rec)
	err = os.MkdirAll(talkoot.Dir(), 0o700)
	if err == nil {
		err = privfs.WriteFile(path, raw)
	}
	if err != nil {
		// 🔑 A marked session that no record names would keep the manager's
		// tools beside the next one, so it goes with the failed write.
		_ = w.DeleteSession(ctx, info.ID)
		return ctrlproto.SessionInfo{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "record the team manager: %v", err)
	}
	return info, nil
}

// readManagerRecord reads the record, and drops each entry whose session id
// is not a bare id. A record that does not read is empty.
func readManagerRecord(path string) managerRecord {
	rec := managerRecord{Sessions: map[string]string{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return rec
	}
	var got managerRecord
	if json.Unmarshal(raw, &got) != nil {
		return rec
	}
	for dir, id := range got.Sessions {
		if validSessionID(id) {
			rec.Sessions[dir] = id
		}
	}
	return rec
}

// isManagerSession reports whether id names a session marked as the manager,
// live or on disk.
func (w *Workspace) isManagerSession(id string) bool {
	if s := w.existing(id); s != nil {
		return s.isManager()
	}
	if !validSessionID(id) {
		return false
	}
	_, meta, err := session.ReadSessionMeta(w.sessionPath(id))
	return err == nil && meta.Manager
}

// isManager reports whether the session is the team manager.
func (s *wsSession) isManager() bool {
	return s != nil && s.sess != nil && s.sess.Meta.Manager
}

// managerGreetingMessage appends the static greeting to a new manager
// session's transcript, and returns it.
func managerGreetingMessage(sess *session.Session) (provider.Message, error) {
	m := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: managerGreeting}},
		Time:    time.Now(),
		Meta:    map[string]string{core.MetaSource: managerGreetingSource},
	}
	return m, sess.AppendMessage(m)
}

// talkootManager is the manager session's handle on the daemon's talkoots.
type talkootManager struct {
	w       *Workspace
	session string
}

var _ tools.TalkootManager = talkootManager{}

func (m talkootManager) Teams() ([]tools.ManagerTeam, error) {
	list, err := m.w.talkootList(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]tools.ManagerTeam, 0, len(list))
	for _, sum := range list {
		tm := tools.ManagerTeam{ID: sum.ID, Title: sum.Title, State: string(sum.State), Running: sum.Running, Problem: sum.Problem}
		if sum.Running {
			if run, err := m.w.talkootRunOf(sum.ID); err == nil {
				tm.Members = run.roster.Load().Members
			}
		}
		out = append(out, tm)
	}
	return out, nil
}

func (m talkootManager) Roster(id string) (tools.RecruitView, error) {
	v, err := talkootRecruit{w: m.w, session: m.session, talkoot: id}.Roster()
	if errors.Is(err, ErrTalkootNotFound) || errors.Is(err, fs.ErrNotExist) {
		return v, errors.New("talkoot: no running talkoot has that id; talkoot_teams lists them")
	}
	return v, err
}

func (m talkootManager) Propose(id string, ops []talkoot.Op, draft, why string) (talkoot.Proposal, error) {
	return m.w.talkootPropose(id, talkoot.ManagerPrefix+m.session, ops, "", why, draft, nil)
}

// TalkootDrafts lists the team manager's drafts for this workspace.
func (w *Workspace) TalkootDrafts(ctx context.Context) (ctrlproto.TalkootDraftsResult, error) {
	if !config.TalkootEnabled() {
		return ctrlproto.TalkootDraftsResult{}, talkootWireErr(talkoot.ErrDisabled, ctrlproto.CodeInternal)
	}
	list, err := talkoot.Drafts(w.cwd)
	if err != nil {
		return ctrlproto.TalkootDraftsResult{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	out := ctrlproto.TalkootDraftsResult{Drafts: make([]ctrlproto.TalkootDraft, 0, len(list))}
	for _, d := range list {
		row := ctrlproto.TalkootDraft{ID: d.ID, Text: d.Text, Why: d.Why, Problem: d.Problem}
		if !d.At.IsZero() {
			row.At = d.At.UTC().Format(time.RFC3339)
		}
		out.Drafts = append(out.Drafts, row)
	}
	return out, nil
}

// DiscardTalkootDraft removes one draft of this workspace. A draft of another
// workspace reads as missing.
func (w *Workspace) DiscardTalkootDraft(ctx context.Context, p ctrlproto.TalkootRef) error {
	if !config.TalkootEnabled() {
		return talkootWireErr(talkoot.ErrDisabled, ctrlproto.CodeInternal)
	}
	gone, err := talkoot.DeleteDraft(w.cwd, p.ID)
	if err != nil {
		return talkootWireErr(err, ctrlproto.CodeInternal)
	}
	if !gone {
		return talkootWireErr(fmt.Errorf("%w: no draft named %q", ErrTalkootNotFound, p.ID), ctrlproto.CodeInternal)
	}
	w.BroadcastAll(ctrlproto.TalkootsChangedEvent())
	return nil
}

// Templates lists the templates this workspace can see, the same list the
// builder offers the person.
func (m talkootManager) Templates() ([]talkoot.Template, error) {
	if !config.TalkootEnabled() {
		return nil, talkoot.ErrDisabled
	}
	return m.w.talkootTemplates(), nil
}

// Draft checks a new team with the builder's preview and saves it as a draft
// of this workspace.
//
// 🔑 A draft writes no roster. The person reviews it in the builder, and only
// the person's Create makes the team. So the check here only keeps a draft
// that Create would refuse out of the person's way.
func (m talkootManager) Draft(id, text, why string) (tools.ManagerDraft, error) {
	if len(why) > talkoot.MaxDraftWhyBytes {
		return tools.ManagerDraft{}, fmt.Errorf("why is %d bytes; keep it under %d", len(why), talkoot.MaxDraftWhyBytes)
	}
	pv, raw, err := m.w.talkootPreview(ctrlproto.TalkootPreviewParams{ID: id, Text: text})
	if err != nil {
		return tools.ManagerDraft{}, err
	}
	if talkootExists(id) {
		return tools.ManagerDraft{}, fmt.Errorf("a talkoot named %s already exists; propose a change to it with talkoot_propose instead", id)
	}
	if len(pv.Problems) > 0 {
		return tools.ManagerDraft{}, fmt.Errorf("the draft has problems: %s", strings.Join(pv.Problems, "; "))
	}
	for _, mb := range pv.Members {
		if !mb.Available {
			return tools.ManagerDraft{}, fmt.Errorf("member %s cannot run here: %s", mb.ID, mb.Problem)
		}
	}
	r, err := talkoot.Parse(raw, id+"/"+talkoot.FileName)
	if err != nil {
		return tools.ManagerDraft{}, err
	}
	replaced, err := talkoot.SaveDraft(talkoot.Draft{
		ID: id, Home: m.w.cwd, Text: text, Why: why, At: time.Now().UTC(), Session: m.session,
	})
	if err != nil {
		return tools.ManagerDraft{}, err
	}
	m.w.BroadcastAll(ctrlproto.TalkootsChangedEvent())
	return tools.ManagerDraft{ID: id, Budget: r.BudgetUSDPerDay, Members: r.Members, Replaced: replaced}, nil
}
