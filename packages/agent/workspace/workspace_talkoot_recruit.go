package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/worker"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
)

// The Talkoot recruiter (TKT-01M39VWMQ): a native session bound to one
// talkoot that holds no seat, such as a Hautoja session. It reads the roster
// and proposes a member, with a new persona when the job needs one. A person
// decides the proposal, and only an approval writes the persona or the
// roster.
//
// 🔑 The binding lives in the session's meta (Meta.Recruit), not in a seat.
// A recruiter has no room address, no budget line, and no chain, so nothing
// routes to it and it costs nothing while nobody talks to it.

// RecruiterPersona is the persona a recruiter session runs when its creator
// names none.
const RecruiterPersona = "hautoja"

// recruitGreeting is the recruiter's first message. It is static text, so
// the session makes no model call before the person writes. A creator that
// speaks first writes from its priors (docs/proposals/creator.md).
const recruitGreeting = "I recruit members for the talkoot %s. Tell me the one job the new member does, or the member you want to rework. I will read the roster, ask what I cannot work out, and propose a member for you to approve."

// recruitGreetingSource tags the greeting, so a reader can tell it from a model
// turn.
const recruitGreetingSource = "talkoot:recruit-greeting"

// checkRecruit refuses a recruiter session for a talkoot this daemon does not
// run, or with an immersive spec or persona, and fills in the recruiter
// persona.
func (w *Workspace) checkRecruit(opts *ctrlproto.CreateOpts) error {
	if opts.Experience != "" || opts.Card != "" || opts.World != "" || len(opts.Cast) > 0 || opts.Background != "" || opts.Greeting != 0 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a recruiter session cannot be an immersive session"))
	}
	if _, err := w.talkootRunOf(opts.Recruit); err != nil {
		return talkootWireErr(err, ctrlproto.CodeNotFound)
	}
	if opts.Persona == "" {
		opts.Persona = RecruiterPersona
	}
	// An immersive charter replaces the whole system prompt, and with it the
	// framing the recruiter tools rely on. Resolve reads a path as the session
	// build does, so a persona file cannot slip past as a path.
	if p, err := persona.Resolve(opts.Persona); err == nil && p.Immersive {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a recruiter session cannot run the immersive persona %s", p.Ref()))
	}
	return nil
}

// recruitGreetingMessage appends the static greeting to a new recruiter
// session's transcript, and returns it.
func recruitGreetingMessage(sess *session.Session, talkootID string) (provider.Message, error) {
	m := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf(recruitGreeting, talkootID)}},
		Time:    time.Now(),
		Meta:    map[string]string{core.MetaSource: recruitGreetingSource},
	}
	return m, sess.AppendMessage(m)
}

// recruitOf returns the talkoot a session recruits for, or "".
func (s *wsSession) recruitOf() string {
	if s == nil || s.sess == nil {
		return ""
	}
	return s.sess.Meta.Recruit
}

// talkootRecruit is a recruiter session's handle on its talkoot.
type talkootRecruit struct {
	w       *Workspace
	session string
	talkoot string
}

var _ tools.TalkootRecruit = talkootRecruit{}

func (r talkootRecruit) Propose(ops []talkoot.Op, draft, why string) (talkoot.Proposal, error) {
	return r.w.talkootPropose(r.talkoot, talkoot.RecruiterPrefix+r.session, ops, "", why, draft, nil)
}

func (r talkootRecruit) Roster() (tools.RecruitView, error) {
	run, err := r.w.talkootRunOf(r.talkoot)
	if err != nil {
		return tools.RecruitView{}, err
	}
	v := tools.RecruitView{Talkoot: r.talkoot, Drivers: recruitDrivers()}
	for _, m := range run.roster.Load().Members {
		rm := tools.RecruitMember{Member: m}
		if p, ok := persona.Lookup(m.Persona); ok && m.Persona != "" {
			rm.Summary, rm.AvoidFor = p.Summary, p.AvoidFor
		}
		v.Members = append(v.Members, rm)
	}
	for _, p := range persona.All() {
		v.Personas = append(v.Personas, tools.RecruitPersona{Name: p.Ref(), Summary: p.Summary})
	}
	return v, nil
}

// recruitDrivers lists the drivers this machine can run, native first, and
// what a tools list for each may name. A driver that is not installed is left
// out, so the recruiter never offers it.
func recruitDrivers() []tools.RecruitDriver {
	out := []tools.RecruitDriver{{Name: talkoot.DriverNative, Tools: "any tool name, a name with a trailing * for a prefix, or mcp:<server> for one MCP server"}}
	names := worker.Names()
	sort.Strings(names)
	for _, name := range names {
		b, err := worker.Lookup(name)
		if err != nil || (b.Installed != nil && !b.Installed()) {
			continue
		}
		out = append(out, tools.RecruitDriver{Name: name, Tools: driverToolNames(b)})
	}
	return out
}

// driverToolNames says which names a tools list for a backend may give, by
// asking the backend itself, so the answer cannot drift from what the roster
// accepts.
func driverToolNames(b worker.Backend) string {
	if b.Tools == nil {
		return "no tools list; a member on this driver keeps the full set of its posture"
	}
	var ok []string
	for _, name := range []string{"read", "write", "edit", "bash", "grep", "glob"} {
		if _, err := b.Tools([]string{name}); err == nil {
			ok = append(ok, name)
		}
	}
	if _, err := b.Tools([]string{"mcp:example"}); err == nil {
		ok = append(ok, "mcp:<server>")
	}
	if len(ok) == 0 {
		return "no tools list"
	}
	return "only " + strings.Join(ok, ", ")
}

// checkRecruitOps refuses what a recruiter may not propose: an operation other
// than add, edit, or look, and the posture yolo. A person can still widen a
// member on the card, or propose it themselves.
func checkRecruitOps(ops []talkoot.Op) error {
	for _, op := range ops {
		switch op.Op {
		case "add", "edit", "look":
		default:
			return fmt.Errorf("talkoot: a recruiter proposes add, edit, or look, not %q; a person removes a member", op.Op)
		}
		if v, ok := op.Set["posture"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "yolo") {
			return errors.New("talkoot: a recruiter never proposes the posture yolo; propose the narrowest posture that does the job, and a person can widen it on the card")
		}
	}
	return nil
}

// checkPersonaDraft checks a recruiter's persona file against the bar that
// `terva persona validate` applies, and against the quality bar's machine
// half: a summary, and a non-empty avoid_for. The name must be free in every
// tier, because a user persona outranks a built-in of the same name, and the
// operations must name it.
func checkPersonaDraft(text string, ops []talkoot.Op) (*talkoot.PersonaDraft, error) {
	if len(text) > talkoot.MaxPersonaDraftBytes {
		return nil, fmt.Errorf("talkoot: the persona is %d bytes, above the %d limit", len(text), talkoot.MaxPersonaDraftBytes)
	}
	p, problems := persona.Check([]byte(text), "recruiter draft")
	if strings.TrimSpace(p.Summary) == "" && len(problems) == 0 {
		problems = append(problems, "the frontmatter needs a summary: the member's one job in one sentence")
	}
	if len(p.AvoidFor) == 0 && len(problems) == 0 {
		problems = append(problems, "the frontmatter needs avoid_for: what the member refuses, and who does it instead")
	}
	if p.Immersive && len(problems) == 0 {
		problems = append(problems, "a member's persona cannot be immersive: an immersive charter replaces the system prompt, and with it the member's Talkoot tools and seat")
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("talkoot: the persona does not pass: %s", strings.Join(problems, "; "))
	}
	d := &talkoot.PersonaDraft{Name: p.Name, Text: text}
	if taken := personaTaken(d); taken != "" {
		return nil, fmt.Errorf("talkoot: the persona name %q is taken by %s; give the new persona a name no persona has", d.Name, taken)
	}
	if !opsUseDraft(ops, d) {
		return nil, fmt.Errorf("talkoot: no operation sets persona to %q, so the proposal would write a persona that no member uses", d.Name)
	}
	return d, nil
}

// draftPersona parses a draft as the library will read it once an approval
// writes it: filed under the path the write takes. Its Ref and Matches then
// answer as Lookup will after the write. The path is the second result.
func draftPersona(d *talkoot.PersonaDraft) (persona.Persona, string, error) {
	p, err := persona.Parse(d.Text, "recruiter draft")
	if err != nil {
		return persona.Persona{}, "", err
	}
	// ⚠️ Path files a persona under the stem of its Source. A draft has no
	// file yet, so it files under the slug of its name, as a new persona does.
	p.Source = ""
	dest, err := persona.Path(p)
	if err != nil {
		return persona.Persona{}, "", err
	}
	p.Source = dest
	return p, dest, nil
}

// personaTaken names the persona that already answers to the draft's name or
// its reference, or returns "". A draft that does not parse is taken by
// nothing, and the write refuses it.
func personaTaken(d *talkoot.PersonaDraft) string {
	p, _, err := draftPersona(d)
	if err != nil {
		return ""
	}
	for _, q := range []string{p.Name, p.Ref()} {
		if found, ok := persona.Lookup(q); ok {
			return found.Ref()
		}
	}
	return ""
}

// opsUseDraft reports whether an add or an edit sets persona to the draft.
func opsUseDraft(ops []talkoot.Op, d *talkoot.PersonaDraft) bool {
	if d == nil {
		return false
	}
	p, _, err := draftPersona(d)
	if err != nil {
		return false
	}
	for _, op := range ops {
		if v, ok := op.Set["persona"].(string); ok && p.Matches(v) {
			return true
		}
	}
	return false
}

// envWithDraft is talkootEnv, with the draft's name resolving as if it were
// written, so a proposal can be checked before the file exists.
func envWithDraft(d *talkoot.PersonaDraft) talkoot.Env {
	env := talkootEnv()
	if d == nil {
		return env
	}
	p, _, err := draftPersona(d)
	if err != nil {
		return env
	}
	exists := env.PersonaExists
	env.PersonaExists = func(ref string) bool { return p.Matches(ref) || exists(ref) }
	return env
}

// writePersonaDraft writes an approved draft to the persona library, byte for
// byte as the card showed it, and returns its path. It never replaces a file:
// a persona that took the name since the proposal fails the approval.
func writePersonaDraft(d *talkoot.PersonaDraft) (string, error) {
	_, dest, err := draftPersona(d)
	if err != nil {
		return "", err
	}
	if err := privfs.MkdirAll(filepath.Dir(dest)); err != nil {
		return "", err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("talkoot: write the persona %s: %w", d.Name, err)
	}
	if _, err := f.WriteString(d.Text); err != nil {
		f.Close()
		os.Remove(dest)
		return "", fmt.Errorf("talkoot: write the persona %s: %w", d.Name, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("talkoot: write the persona %s: %w", d.Name, err)
	}
	return dest, nil
}
