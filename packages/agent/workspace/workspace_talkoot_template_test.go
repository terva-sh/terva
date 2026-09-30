package workspace

import (
	"errors"
	"os"
	"path/filepath"
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
