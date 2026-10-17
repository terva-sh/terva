package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// repoTeam is a repository template with a member whose driver no machine
// has.
const repoTeam = `---
name: repo-team
description: A lead and an outside reviewer.
budget_usd_per_day: 6
members:
  - id: lead
    role: coordinator
    persona: mieli
  - id: outside
    role: specialist
    reviewer: true
    driver: acp:not-installed
    turns_per_day: 10
---

Review everything.
`

func writeRepoTemplate(t *testing.T, cwd, name, body string) {
	t.Helper()
	dir := talkoot.RepoTemplatesDir(cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestATalkootIsCreatedFromTheBuiltinCodingTeam(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()

	list, err := w.TalkootTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var coding *ctrlproto.TalkootTemplate
	for i, tm := range list.Templates {
		if tm.Name == "coding" {
			coding = &list.Templates[i]
		}
	}
	if coding == nil || coding.Source != talkoot.SourceBuiltin || coding.Members != 6 || coding.Problem != "" {
		t.Fatalf("templates = %+v", list.Templates)
	}

	pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "coding", ID: "team"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Problems) != 0 {
		t.Fatalf("the built-in team has problems here: %v", pv.Problems)
	}
	if !sameDir(pv.Home, cwd) || pv.BudgetUSDPerDay != 20 || len(pv.Members) != 6 || pv.Digest == "" {
		t.Fatalf("preview = %+v", pv)
	}
	for _, m := range pv.Members {
		if !m.Available || m.Posture == "" || m.Workspace == "" || m.Driver == "" {
			t.Errorf("member %+v: the preview must show every member's driver, posture, and workspace", m)
		}
	}
	if _, err := os.Stat(filepath.Join(talkoot.Dir(), "team")); !os.IsNotExist(err) {
		t.Fatalf("a preview wrote the talkoot: %v", err)
	}

	// Create refuses without the preview's digest.
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "team", Template: "coding"}); err == nil {
		t.Fatal("create from a template succeeded without a digest")
	}
	v, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "team", Template: "coding", Digest: pv.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "team" || len(v.Members) != 6 || v.Text != pv.Text {
		t.Fatalf("create returned %+v", v)
	}
	// The same preview now reports the id as taken.
	again, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "coding", ID: "team"})
	if err != nil || len(again.Problems) == 0 {
		t.Errorf("a second preview of a taken id: %+v, %v", again.Problems, err)
	}
}

// The person chose six built-in templates on 2026-10-14 (TKT-01M4YQN86).
// Each one must preview clean on a fresh install: real personas, the roster
// rules, and native drivers only.
func TestEveryBuiltinTemplatePreviewsClean(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	list, err := w.TalkootTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tm := range list.Templates {
		if tm.Source != talkoot.SourceBuiltin {
			continue
		}
		got = append(got, tm.Name)
		if tm.Problem != "" || tm.Description == "" || tm.Title == "" {
			t.Errorf("template %s = %+v", tm.Name, tm)
			continue
		}
		pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: tm.Name, ID: "t-" + tm.Name})
		if err != nil {
			t.Errorf("%s: %v", tm.Name, err)
			continue
		}
		if len(pv.Problems) != 0 {
			t.Errorf("%s has problems on a fresh install: %v", tm.Name, pv.Problems)
		}
		for _, m := range pv.Members {
			if !m.Available || m.Driver != talkoot.DriverNative {
				t.Errorf("%s: member %s uses driver %s; a built-in team must run on any install", tm.Name, m.ID, m.Driver)
			}
		}
	}
	want := []string{"coding", "decision-council", "documentation", "personal-assistant", "research", "review-pair"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("built-in templates = %v, want %v", got, want)
	}
}

// The team builder (TKT-01M4YQN88) previews a roster the person composed,
// with no template, and creates it through talkoot.create text.
func TestAComposedRosterPreviewsAndCreatesWithoutATemplate(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	composed := "---\nname: duo\nbudget_usd_per_day: 3\nmembers:\n  - id: lead\n    role: coordinator\n    persona: mieli\n  - id: helper\n    role: specialist\n    persona: kirjuri\n    posture: auto-edit\n  - id: second\n    role: specialist\n    posture: auto-edit\n---\n\nWork in pairs.\n"

	// Two writers in the shared checkout: the preview names the rule.
	bad, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Text: composed, ID: "duo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(bad.Problems, "; "), "members helper, second can all write to the shared checkout") {
		t.Fatalf("two shared writers previewed without a problem: %v", bad.Problems)
	}

	fixed := strings.Replace(composed, "  - id: second\n    role: specialist\n    posture: auto-edit\n", "  - id: second\n    role: specialist\n", 1)
	pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Text: fixed, ID: "duo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Problems) != 0 || !sameDir(pv.Home, cwd) || len(pv.Members) != 3 || pv.Template.Source != talkoot.SourceComposed {
		t.Fatalf("preview = %+v", pv)
	}
	v, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "duo", Text: pv.Text})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "duo" || len(v.Members) != 3 || !strings.Contains(v.Text, "Work in pairs.") {
		t.Fatalf("create returned %+v", v)
	}

	// The web composer quotes every scalar and writes lists and marks in flow
	// style (builder.ts composeRoster). That shape must preview clean too.
	web := "---\nname: \"trio\"\nbudget_usd_per_day: 4\nmembers:\n  - id: \"lead\"\n    role: \"coordinator\"\n    persona: \"mieli\"\n    posture: \"auto-edit\"\n  - id: \"check\"\n    role: \"specialist\"\n    mark: {shape: \"circle\", color: \"#336699\"}\n    reviewer: true\n    turns_per_day: 10\n    tools: [\"read\", \"grep\"]\n---\n\nKeep it short.\n"
	wv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Text: web, ID: "trio"})
	if err != nil || len(wv.Problems) != 0 || len(wv.Members) != 2 {
		t.Fatalf("the web composer's roster: %+v, %v", wv, err)
	}

	// A composed roster may not carry a home or a waiver, and not both forms.
	for name, p := range map[string]ctrlproto.TalkootPreviewParams{
		"home":   {Text: strings.Replace(fixed, "name: duo\n", "name: duo\nhome: /elsewhere\n", 1), ID: "x"},
		"waiver": {Text: strings.Replace(fixed, "name: duo\n", "name: duo\nteam_budget_waived: true\n", 1), ID: "x"},
		"both":   {Text: fixed, Template: "coding", ID: "x"},
	} {
		if _, err := w.PreviewTalkoot(ctx, p); err == nil {
			t.Errorf("a composed roster with %s previewed", name)
		}
	}
}

// The team builder starts from a template by copying the preview's member
// rows. A member field the wire row lacks would drop out of the composed
// roster in silence, so the row must carry every field the roster has.
func TestThePreviewRowCarriesEveryMemberField(t *testing.T) {
	tags := map[string]bool{}
	rt := reflect.TypeOf(ctrlproto.TalkootRosterMember{})
	for i := 0; i < rt.NumField(); i++ {
		tags[strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	for _, f := range talkoot.MemberFields {
		if !tags[f.Name] {
			t.Errorf("TalkootRosterMember has no %q field; a builder seeded from a template drops it", f.Name)
		}
	}
}

// Save as template: a running team becomes a user template that the form
// lists, without the home, colour, or waiver of the team it came from.
func TestARunningTeamIsSavedAsAUserTemplate(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "review-pair", ID: "pair"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "pair", Template: "review-pair", Digest: pv.Digest}); err != nil {
		t.Fatal(err)
	}
	color := "#336699"
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "pair", By: "drew", Color: &color}); err != nil {
		t.Fatal(err)
	}

	got, err := w.SaveTalkootTemplate(ctx, ctrlproto.TalkootSaveTemplateParams{ID: "pair", Name: "our-pair", Description: "Our pair."})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "our-pair" || got.Source != talkoot.SourceUser || got.Members != 2 || got.Description != "Our pair." || got.Problem != "" {
		t.Fatalf("saved template = %+v", got)
	}
	raw, err := os.ReadFile(filepath.Join(talkoot.TemplatesDir(), "our-pair.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"home:", "color:", cwd} {
		if strings.Contains(string(raw), gone) {
			t.Errorf("the saved template keeps %q:\n%s", gone, raw)
		}
	}
	list, err := w.TalkootTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, tm := range list.Templates {
		listed = listed || (tm.Name == "our-pair" && tm.Source == talkoot.SourceUser)
	}
	if !listed {
		t.Fatalf("the form does not list the saved template: %+v", list.Templates)
	}
	again, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "our-pair", ID: "pair-two"})
	if err != nil || len(again.Problems) != 0 {
		t.Fatalf("a preview of the saved template: %v, %v", again.Problems, err)
	}

	var ce *ctrlproto.Error
	if _, err := w.SaveTalkootTemplate(ctx, ctrlproto.TalkootSaveTemplateParams{ID: "pair", Name: "our-pair"}); !errors.As(err, &ce) || ce.Code != ctrlproto.CodeConflict {
		t.Errorf("a second save of one name: %v", err)
	}
	if _, err := w.SaveTalkootTemplate(ctx, ctrlproto.TalkootSaveTemplateParams{ID: "nobody", Name: "x"}); !errors.As(err, &ce) || ce.Code != ctrlproto.CodeNotFound {
		t.Errorf("a save of a team that does not exist: %v", err)
	}
}

// Open question 4 of the proposal: a repository template may name a driver
// this machine lacks. The preview marks the member, create refuses until the
// person drops it, and the person's drop changes the digest.
func TestARepoTemplateMemberWithAMissingDriverMustBeDropped(t *testing.T) {
	cwd := talkootHome(t)
	writeRepoTemplate(t, cwd, "review", repoTeam)
	w := openTalkootWorkspace(t, cwd)
	w.trusted.Store(true)
	ctx := t.Context()

	pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "repo:review", ID: "rev", BudgetUSDPerDay: 3})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Template.Source != talkoot.SourceRepo || pv.BudgetUSDPerDay != 3 {
		t.Fatalf("preview = %+v", pv)
	}
	var outside ctrlproto.TalkootPreviewMember
	for _, m := range pv.Members {
		if m.ID == "outside" {
			outside = m
		}
	}
	if outside.Available || !strings.Contains(outside.Problem, "acp:not-installed") {
		t.Fatalf("the member with a missing driver is %+v", outside)
	}
	_, err = w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "rev", Template: "repo:review", BudgetUSDPerDay: 3, Digest: pv.Digest})
	if err == nil || !strings.Contains(err.Error(), "drop it") {
		t.Fatalf("create with an unavailable member: %v", err)
	}

	dropped, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "repo:review", ID: "rev", BudgetUSDPerDay: 3, Drop: []string{"outside"}})
	if err != nil {
		t.Fatal(err)
	}
	if dropped.Digest == pv.Digest || len(dropped.Members) != 1 || len(dropped.Problems) != 0 {
		t.Fatalf("preview after the drop = %+v", dropped)
	}
	v, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "rev", Template: "repo:review", BudgetUSDPerDay: 3, Drop: []string{"outside"}, Digest: dropped.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Members) != 1 || v.BudgetUSDPerDay != 3 {
		t.Fatalf("create returned %+v", v)
	}
}

// 🔑 A digest binds create to the roster the person saw. A template edited
// after the preview, or inputs that differ from the preview's, are refused.
func TestCreateRefusesARosterThatChangedSinceThePreview(t *testing.T) {
	cwd := talkootHome(t)
	body := strings.Replace(repoTeam, "driver: acp:not-installed\n    turns_per_day: 10\n", "persona: koestaja\n", 1)
	writeRepoTemplate(t, cwd, "review", body)
	w := openTalkootWorkspace(t, cwd)
	w.trusted.Store(true)
	ctx := t.Context()

	pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "repo:review", ID: "rev"})
	if err != nil || len(pv.Problems) != 0 {
		t.Fatalf("preview: %+v, %v", pv.Problems, err)
	}
	_, err = w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "rev", Template: "repo:review", BudgetUSDPerDay: 50, Digest: pv.Digest})
	if talkootCode(err) != ctrlproto.CodeConflict {
		t.Errorf("create with a different budget: %v", err)
	}
	writeRepoTemplate(t, cwd, "review", body+"\nAlso ship it.\n")
	_, err = w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "rev", Template: "repo:review", Digest: pv.Digest})
	if talkootCode(err) != ctrlproto.CodeConflict {
		t.Errorf("create after the template changed: %v", err)
	}
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "rev", Template: "repo:review", Text: "---", Digest: pv.Digest}); err == nil {
		t.Error("create took both text and a template")
	}
	if _, err := os.Stat(filepath.Join(talkoot.Dir(), "rev")); !os.IsNotExist(err) {
		t.Errorf("a refused create left the talkoot behind: %v", err)
	}
}

func TestTemplatesAnswerUnsupportedWhileTalkootIsOff(t *testing.T) {
	cwd := talkootHome(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(`{"talkoot_enabled": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := openTalkootWorkspace(t, cwd)
	if _, err := w.TalkootTemplates(t.Context()); talkootCode(err) != ctrlproto.CodeUnsupported {
		t.Errorf("templates while off: %v", err)
	}
	if _, err := w.PreviewTalkoot(t.Context(), ctrlproto.TalkootPreviewParams{Template: "coding", ID: "x"}); talkootCode(err) != ctrlproto.CodeUnsupported {
		t.Errorf("preview while off: %v", err)
	}
}

// A repository template grants what a roster grants, except the home. In an
// untrusted workspace it is listed with the reason, and it cannot be used.
func TestARepoTemplateNeedsATrustedWorkspace(t *testing.T) {
	cwd := talkootHome(t)
	writeRepoTemplate(t, cwd, "review", strings.Replace(repoTeam, "driver: acp:not-installed\n    turns_per_day: 10\n", "persona: koestaja\n", 1))
	// A file that does not parse. An untrusted workspace lists it by name and
	// never reads it, so its problem is the verdict, not a parse error.
	writeRepoTemplate(t, cwd, "broken", "not a template")
	w := openTalkootWorkspace(t, cwd)
	w.trusted.Store(false)
	ctx := t.Context()

	list, err := w.TalkootTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var repo ctrlproto.TalkootTemplate
	for _, tm := range list.Templates {
		if tm.Name == "repo:review" {
			repo = tm
		}
	}
	if repo.Problem != untrustedRepoTemplate {
		t.Fatalf("repo:review in an untrusted workspace = %+v", repo)
	}
	for _, tm := range list.Templates {
		if tm.Name == "repo:broken" && tm.Problem != untrustedRepoTemplate {
			t.Errorf("an untrusted workspace parsed repo:broken: %+v", tm)
		}
	}
	if _, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "repo:review", ID: "rev"}); err == nil {
		t.Error("an untrusted repository template previewed")
	}
	// The built-in tier does not depend on the verdict.
	if pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "coding", ID: "team"}); err != nil || len(pv.Problems) != 0 {
		t.Errorf("the built-in team in an untrusted workspace: %+v, %v", pv.Problems, err)
	}

	w.trusted.Store(true)
	if pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "repo:review", ID: "rev"}); err != nil || len(pv.Problems) != 0 {
		t.Errorf("a trusted repository template: %+v, %v", pv, err)
	}
}

// The #1478 re-review: the digest binds the id, and a taken id answers
// conflict in both forms of create.
func TestTheDigestBindsTheIDAndATakenIDIsAConflict(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	pv, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "coding", ID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "two", Template: "coding", Digest: pv.Digest})
	if talkootCode(err) != ctrlproto.CodeConflict || !strings.Contains(err.Error(), "preview again") {
		t.Fatalf("create under an id that was not previewed: %v", err)
	}
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "one", Template: "coding", Digest: pv.Digest}); err != nil {
		t.Fatal(err)
	}
	again, err := w.PreviewTalkoot(ctx, ctrlproto.TalkootPreviewParams{Template: "coding", ID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "one", Template: "coding", Digest: again.Digest})
	if talkootCode(err) != ctrlproto.CodeConflict || !errors.Is(err, ErrTalkootExists) {
		t.Errorf("create from a template under a taken id: %v", err)
	}
}
