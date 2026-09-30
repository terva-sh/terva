package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/worker"
)

// Talkoot templates: list them, preview the roster one becomes, and create a
// talkoot from the preview. See talkoot/template.go for the file and its tiers.

// ErrPreviewStale is returned when a create's digest does not match the
// roster the template makes now.
var ErrPreviewStale = errors.New("talkoot: the roster changed since the preview; preview again and create with the new digest")

func (w *Workspace) TalkootTemplates(ctx context.Context) (ctrlproto.TalkootTemplatesResult, error) {
	if !config.TalkootEnabled() {
		return ctrlproto.TalkootTemplatesResult{}, talkootWireErr(talkoot.ErrDisabled, ctrlproto.CodeInternal)
	}
	list := w.talkootTemplates()
	out := ctrlproto.TalkootTemplatesResult{Templates: make([]ctrlproto.TalkootTemplate, 0, len(list))}
	for _, t := range list {
		out.Templates = append(out.Templates, wireTemplate(t))
	}
	return out, nil
}

func (w *Workspace) PreviewTalkoot(ctx context.Context, p ctrlproto.TalkootPreviewParams) (ctrlproto.TalkootPreview, error) {
	pv, _, err := w.talkootPreview(p)
	if err != nil {
		return ctrlproto.TalkootPreview{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return pv, nil
}

// createFromTemplate is talkoot.create's template form. It recomputes the
// preview, so what it writes is exactly what the caller was shown.
func (w *Workspace) createFromTemplate(ctx context.Context, p ctrlproto.TalkootCreateParams) (talkootView, error) {
	if p.Text != "" {
		return talkootView{}, errors.New("talkoot: send text or a template, not both")
	}
	if p.Digest == "" {
		return talkootView{}, errors.New("talkoot: creating from a template needs the digest from talkoot.preview")
	}
	pv, text, err := w.talkootPreview(ctrlproto.TalkootPreviewParams{
		Template: p.Template, ID: p.ID, Home: p.Home, BudgetUSDPerDay: p.BudgetUSDPerDay, Drop: p.Drop,
	})
	if err != nil {
		return talkootView{}, err
	}
	if pv.Digest != p.Digest {
		return talkootView{}, ErrPreviewStale
	}
	// The same code the text form answers for a taken id.
	if talkootExists(p.ID) {
		return talkootView{}, fmt.Errorf("%w: %s already exists", ErrTalkootExists, p.ID)
	}
	for _, m := range pv.Members {
		if !m.Available {
			return talkootView{}, fmt.Errorf("talkoot: member %s cannot run here (%s); drop it and preview again", m.ID, m.Problem)
		}
	}
	if len(pv.Problems) > 0 {
		return talkootView{}, fmt.Errorf("talkoot: the roster has problems: %s", strings.Join(pv.Problems, "; "))
	}
	return w.talkootCreate(ctx, p.ID, text)
}

// talkootPreview builds the roster a template becomes, and reports every
// problem it has here without writing anything. The text is what create
// writes, and the digest is its hash.
func (w *Workspace) talkootPreview(p ctrlproto.TalkootPreviewParams) (ctrlproto.TalkootPreview, []byte, error) {
	if !config.TalkootEnabled() {
		return ctrlproto.TalkootPreview{}, nil, talkoot.ErrDisabled
	}
	if !talkoot.ValidID(p.ID) {
		return ctrlproto.TalkootPreview{}, nil, fmt.Errorf("talkoot: id %q must be lower case letters, digits, and dashes, starting with a letter", p.ID)
	}
	tmpl, ok := w.lookupTalkootTemplate(p.Template)
	if !ok {
		return ctrlproto.TalkootPreview{}, nil, fmt.Errorf("%w: no template named %q", ErrTalkootNotFound, p.Template)
	}
	home := p.Home
	if strings.TrimSpace(home) == "" {
		home = w.cwd
	}
	text, err := talkoot.Instantiate(tmpl, home, p.BudgetUSDPerDay, p.Drop)
	if err != nil {
		return ctrlproto.TalkootPreview{}, nil, err
	}
	r, err := talkoot.Parse(text, p.ID+"/"+talkoot.FileName)
	if err != nil {
		return ctrlproto.TalkootPreview{}, nil, err
	}
	// The digest covers the id as well as the text, so create cannot take a
	// preview of one id to write another.
	sum := sha256.Sum256(append([]byte(p.ID+"\n"), text...))
	pv := ctrlproto.TalkootPreview{
		Template: wireTemplate(tmpl), ID: p.ID, Home: r.Home, BudgetUSDPerDay: r.BudgetUSDPerDay,
		Members: make([]ctrlproto.TalkootPreviewMember, 0, len(r.Members)),
		Text:    string(text), Digest: hex.EncodeToString(sum[:]),
	}
	for _, m := range r.Members {
		row := ctrlproto.TalkootPreviewMember{TalkootRosterMember: wireRosterMember(m), Available: true}
		if m.Driver != talkoot.DriverNative {
			if _, err := worker.Lookup(m.Driver); err != nil {
				row.Available = false
				row.Problem = fmt.Sprintf("driver %q is not installed here", m.Driver)
			}
		}
		pv.Members = append(pv.Members, row)
	}
	if _, err := w.parseRoster(p.ID, text); err != nil {
		var probs *talkoot.Problems
		if errors.As(err, &probs) {
			pv.Problems = append(pv.Problems, probs.List...)
		} else {
			pv.Problems = append(pv.Problems, err.Error())
		}
	}
	if talkootExists(p.ID) {
		pv.Problems = append(pv.Problems, fmt.Sprintf("a talkoot named %s already exists", p.ID))
	}
	return pv, text, nil
}

// untrustedRepoTemplate is the problem a repository template carries while
// the workspace is not trusted.
const untrustedRepoTemplate = "this workspace is not trusted, so its templates cannot be used; trust it first"

// talkootTemplates lists every template. A repository template is listed
// whatever the verdict, so a person sees it. It can be used only in a trusted
// workspace.
//
// 🔑 A repository template carries every field of a roster that grants
// authority except home: postures, tools, drivers, and a charter that every
// member reads as its prompt. The preview digest proves that a caller asked
// for a preview, not that a person read it. So the repository tier also needs
// the verdict that gates every other repository-controlled setting.
//
// An untrusted checkout's templates are listed by name and never read: the
// daemon does not parse files a person has not trusted.
func (w *Workspace) talkootTemplates() []talkoot.Template {
	if w.Trusted() {
		return talkoot.Templates(w.cwd)
	}
	list := talkoot.Templates("")
	for _, name := range talkoot.RepoTemplateNames(w.cwd) {
		list = append(list, talkoot.Template{Name: name, Source: talkoot.SourceRepo, Problem: untrustedRepoTemplate})
	}
	return list
}

// lookupTalkootTemplate finds one template. It reads the repository tier only
// for a repo: name, and only in a trusted workspace.
func (w *Workspace) lookupTalkootTemplate(name string) (talkoot.Template, bool) {
	if strings.HasPrefix(name, talkoot.RepoPrefix) && !w.Trusted() {
		for _, n := range talkoot.RepoTemplateNames(w.cwd) {
			if n == name {
				return talkoot.Template{Name: name, Source: talkoot.SourceRepo, Problem: untrustedRepoTemplate}, true
			}
		}
		return talkoot.Template{}, false
	}
	return talkoot.LookupTemplate(w.cwd, name)
}

func talkootExists(id string) bool {
	_, err := os.Stat(filepath.Join(talkoot.Dir(), id))
	return err == nil
}

func wireTemplate(t talkoot.Template) ctrlproto.TalkootTemplate {
	return ctrlproto.TalkootTemplate{
		Name: t.Name, Title: t.Title, Description: t.Description, Source: t.Source,
		Members: t.Members, Problem: t.Problem,
	}
}

func wireRosterMember(m talkoot.Member) ctrlproto.TalkootRosterMember {
	return ctrlproto.TalkootRosterMember{
		ID: m.ID, Role: m.Role, Title: m.Title, Mark: wireMark(m.Mark), Persona: m.Persona, Driver: m.Driver,
		Model: m.Model, Tier: m.Tier, Posture: m.Posture, Workspace: m.Workspace,
		Reviewer: m.Reviewer, BudgetUSDPerDay: m.BudgetUSDPerDay, TurnsPerDay: m.TurnsPerDay,
		Tools: m.Tools,
	}
}
