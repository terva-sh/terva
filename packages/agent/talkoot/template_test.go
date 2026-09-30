package talkoot

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

const smallTemplate = `---
name: small
title: Small team
description: Two members.
budget_usd_per_day: 10
members:
  - id: helm
    role: coordinator
    persona: mieli
  - id: gage
    role: specialist
    persona: koestaja
    driver: acp:gemini
    turns_per_day: 20
---

Keep it short.
`

// isolatedHome points TERVA_HOME at an empty directory, so the user and
// extension tiers hold only what a test writes.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	return home
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// builtinEnv knows every built-in persona the coding team names, and no
// worker driver.
func builtinEnv() Env {
	env := fakeEnv()
	env.PersonaExists = func(ref string) bool {
		return slices.Contains([]string{"mieli", "arkkitehti", "koestaja", "vartija", "kirjuri"}, ref)
	}
	return env
}

func TestTheBuiltinCodingTeamIsAValidRoster(t *testing.T) {
	isolatedHome(t)
	tmpl, ok := LookupTemplate("", "coding")
	if !ok {
		t.Fatal("the built-in coding template is missing")
	}
	if tmpl.Problem != "" || tmpl.Source != SourceBuiltin || tmpl.Description == "" {
		t.Fatalf("template = %+v", tmpl)
	}
	text, err := Instantiate(tmpl, "/src/app", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(text, "coding.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(r, builtinEnv()); err != nil {
		t.Fatalf("the coding team does not validate: %v", err)
	}
	var writers []string
	for _, m := range r.Members {
		if m.Driver != DriverNative {
			t.Errorf("%s uses driver %s; the built-in team must run on any install", m.ID, m.Driver)
		}
		if m.Writes() {
			writers = append(writers, m.ID)
		}
	}
	if !slices.Equal(writers, []string{"developer"}) {
		t.Errorf("writers = %v, want only the developer", writers)
	}
	if r.Home != "/src/app" || r.BudgetUSDPerDay != 20 || r.Charter == "" {
		t.Errorf("home %q, budget %v, charter %q", r.Home, r.BudgetUSDPerDay, r.Charter)
	}
}

func TestInstantiateFillsHomeAndBudgetAndDropsMembers(t *testing.T) {
	tmpl := parseTemplate("small", []byte(smallTemplate), SourceUser, "small.md")
	if tmpl.Problem != "" {
		t.Fatal(tmpl.Problem)
	}
	if tmpl.Title != "Small team" || tmpl.Members != 2 || tmpl.Description != "Two members." {
		t.Fatalf("template = %+v", tmpl)
	}
	text, err := Instantiate(tmpl, "~/src/app", 7.5, []string{"gage"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "description") {
		t.Errorf("the roster keeps the template's description:\n%s", text)
	}
	r, err := Parse(text, "small.md")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if r.Home != "~/src/app" || r.BudgetUSDPerDay != 7.5 || len(r.Members) != 1 || r.Members[0].ID != "helm" {
		t.Errorf("roster = %+v", r)
	}
	if r.Charter != "Keep it short." {
		t.Errorf("charter = %q", r.Charter)
	}

	// A zero budget keeps the template's suggestion.
	text, err = Instantiate(tmpl, "/src/app", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := mustParse(t, string(text)); r.BudgetUSDPerDay != 10 || len(r.Members) != 2 {
		t.Errorf("roster = %+v", r)
	}
	// The same inputs make the same bytes, which is what a preview digest needs.
	again, _ := Instantiate(tmpl, "/src/app", 0, nil)
	if string(again) != string(text) {
		t.Error("two instantiations of one template differ")
	}
}

func TestInstantiateRefuses(t *testing.T) {
	tmpl := parseTemplate("small", []byte(smallTemplate), SourceUser, "small.md")
	for name, fn := range map[string]func() error{
		"no home":        func() error { _, err := Instantiate(tmpl, " ", 0, nil); return err },
		"a negative cap": func() error { _, err := Instantiate(tmpl, "/src", -1, nil); return err },
		"an unknown drop": func() error {
			_, err := Instantiate(tmpl, "/src", 0, []string{"nobody"})
			return err
		},
	} {
		if err := fn(); err == nil {
			t.Errorf("%s: Instantiate succeeded", name)
		}
	}
}

func TestATemplateThatSetsHomeIsListedWithItsProblem(t *testing.T) {
	raw := strings.Replace(smallTemplate, "budget_usd_per_day: 10", "home: /etc\nbudget_usd_per_day: 10", 1)
	tmpl := parseTemplate("small", []byte(raw), SourceRepo, "small.md")
	if !strings.Contains(tmpl.Problem, "sets no home") {
		t.Fatalf("problem = %q", tmpl.Problem)
	}
	if _, err := Instantiate(tmpl, "/src", 0, nil); err == nil {
		t.Error("a template with a problem instantiated")
	}
	// An unknown key is refused as it is in a roster.
	raw = strings.Replace(smallTemplate, "budget_usd_per_day", "budget_usd_per_dy", 1)
	if tmpl := parseTemplate("small", []byte(raw), SourceUser, "small.md"); tmpl.Problem == "" {
		t.Error("a mistyped key parsed")
	}
}

func TestTemplateTiersShadowAndTheRepoTierStandsApart(t *testing.T) {
	home := isolatedHome(t)
	repo := testsupport.TempDir(t)
	userCoding := strings.Replace(smallTemplate, "title: Small team", "title: My coding team", 1)
	writeFile(t, filepath.Join(home, TemplatesDirName, "coding.md"), userCoding)
	writeFile(t, filepath.Join(home, TemplatesDirName, "Bad_Name.md"), smallTemplate)
	writeFile(t, filepath.Join(home, TemplatesDirName, "README.md"), "not a template")
	writeFile(t, filepath.Join(RepoTemplatesDir(repo), "coding.md"), smallTemplate)

	byName := map[string]Template{}
	for _, tmpl := range Templates(repo) {
		if _, dup := byName[tmpl.Name]; dup {
			t.Errorf("%s is listed twice", tmpl.Name)
		}
		byName[tmpl.Name] = tmpl
	}
	if got := byName["coding"]; got.Source != SourceUser || got.Title != "My coding team" {
		t.Errorf("coding = %+v, want the user tier's", got)
	}
	if got := byName["repo:coding"]; got.Source != SourceRepo || got.Problem != "" {
		t.Errorf("repo:coding = %+v", got)
	}
	if got := byName["Bad_Name"]; got.Problem == "" {
		t.Errorf("a bad file name is listed without a problem: %+v", got)
	}
	if _, ok := byName["README"]; ok {
		t.Error("a README is listed as a template")
	}
	// LookupTemplate reads the repository tier only for a repo: name.
	if got, ok := LookupTemplate(repo, "coding"); !ok || got.Source != SourceUser {
		t.Errorf("LookupTemplate(coding) = %+v, %v", got, ok)
	}
	if _, ok := LookupTemplate("", "repo:coding"); ok {
		t.Error("a repo template resolved with no checkout")
	}
}

// 🚨 A repository controls its template files, and the preview shows their
// text. A symlink must not turn the preview into a way to read another file.
func TestARepoTemplateCannotBeASymlink(t *testing.T) {
	isolatedHome(t)
	repo := testsupport.TempDir(t)
	secret := filepath.Join(testsupport.TempDir(t), "secret.md")
	writeFile(t, secret, smallTemplate)
	if err := os.MkdirAll(RepoTemplatesDir(repo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(RepoTemplatesDir(repo), "team.md")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	tmpl, ok := LookupTemplate(repo, "repo:team")
	if !ok {
		t.Fatal("the symlinked template is not listed")
	}
	if tmpl.Problem == "" || len(tmpl.Raw) != 0 {
		t.Errorf("a symlinked template was read: %+v", tmpl)
	}
}

// An enabled global extension ships templates in its talkoot-templates
// directory. One of the same name as a built-in shadows it, and a user
// template shadows both. A disabled extension ships nothing.
func TestAnExtensionTemplateSitsBetweenTheUserAndTheBuiltinTiers(t *testing.T) {
	home := isolatedHome(t)
	ext := filepath.Join(home, "extensions", "teams")
	writeFile(t, filepath.Join(ext, "extension.json"), `{"name": "teams"}`)
	extCoding := strings.Replace(smallTemplate, "title: Small team", "title: Extension coding team", 1)
	writeFile(t, filepath.Join(ext, TemplatesDirName, "coding.md"), extCoding)
	writeFile(t, filepath.Join(ext, TemplatesDirName, "review.md"), smallTemplate)

	got, ok := LookupTemplate("", "coding")
	if !ok || got.Source != SourceExtPrefix+"teams" || got.Title != "Extension coding team" {
		t.Fatalf("coding = %+v, %v; want the extension's, which shadows the built-in", got, ok)
	}
	writeFile(t, filepath.Join(home, TemplatesDirName, "review.md"), smallTemplate)
	if got, _ := LookupTemplate("", "review"); got.Source != SourceUser {
		t.Errorf("review = %+v; the user tier must shadow the extension", got)
	}

	writeFile(t, filepath.Join(home, "config.json"), `{"disable_extensions": ["teams"]}`)
	if got, _ := LookupTemplate("", "coding"); got.Source != SourceBuiltin {
		t.Errorf("coding = %+v; a disabled extension must ship no template", got)
	}
}

// 🚨 A symlinked directory, .terva or .terva/talkoot, must not lead the
// repository tier outside the checkout (the #1478 review).
func TestARepoTemplateDirectoryCannotLeadOutOfTheCheckout(t *testing.T) {
	isolatedHome(t)
	outside := testsupport.TempDir(t)
	writeFile(t, filepath.Join(outside, "talkoot", "leak.md"), smallTemplate)
	writeFile(t, filepath.Join(outside, "leak.md"), smallTemplate)

	for name, link := range map[string]func(repo string) error{
		".terva": func(repo string) error { return os.Symlink(outside, filepath.Join(repo, ".terva")) },
		".terva/talkoot": func(repo string) error {
			if err := os.MkdirAll(filepath.Join(repo, ".terva"), 0o700); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(repo, ".terva", "talkoot"))
		},
	} {
		repo := testsupport.TempDir(t)
		if err := link(repo); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		for _, tmpl := range Templates(repo) {
			if tmpl.Source == SourceRepo {
				t.Errorf("%s: a symlinked directory listed %+v", name, tmpl)
			}
		}
	}

	// A symlink that stays inside the checkout is the checkout's own layout.
	repo := testsupport.TempDir(t)
	writeFile(t, filepath.Join(repo, "teams", "inside.md"), smallTemplate)
	if err := os.MkdirAll(filepath.Join(repo, ".terva"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "teams"), filepath.Join(repo, ".terva", "talkoot")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if got, ok := LookupTemplate(repo, "repo:inside"); !ok || got.Problem != "" {
		t.Errorf("a template inside the checkout = %+v, %v", got, ok)
	}
}

// A home that YAML would read as another type stays a path.
func TestInstantiateKeepsAnyHomeAString(t *testing.T) {
	tmpl := parseTemplate("small", []byte(smallTemplate), SourceUser, "small.md")
	for _, home := range []string{"~", "null", "true", "1234", "/src/a: b"} {
		text, err := Instantiate(tmpl, home, 0, nil)
		if err != nil {
			t.Fatalf("%q: %v", home, err)
		}
		r, err := Parse(text, "small.md")
		if err != nil {
			t.Fatalf("%q: %v\n%s", home, err, text)
		}
		if r.Home != home {
			t.Errorf("home %q came back as %q", home, r.Home)
		}
	}
}

// The repository tier reads a bounded number of files, whatever the checkout
// holds.
func TestTheRepoTierIsBounded(t *testing.T) {
	isolatedHome(t)
	repo := testsupport.TempDir(t)
	for i := range maxRepoTemplates + 10 {
		writeFile(t, filepath.Join(RepoTemplatesDir(repo), fmt.Sprintf("t%03d.md", i)), smallTemplate)
	}
	n := 0
	for _, tmpl := range Templates(repo) {
		if tmpl.Source == SourceRepo {
			n++
		}
	}
	if n != maxRepoTemplates {
		t.Errorf("the repository tier listed %d templates, want the cap %d", n, maxRepoTemplates)
	}
	if got := len(RepoTemplateNames(repo)); got != maxRepoTemplates {
		t.Errorf("RepoTemplateNames listed %d, want %d", got, maxRepoTemplates)
	}
}

// The #1478 third review: the repo: namespace belongs to the repository, and
// the cap keeps the first templates by name.
func TestTheRepoNamespaceAndCapAreStable(t *testing.T) {
	home := isolatedHome(t)
	repo := testsupport.TempDir(t)
	writeFile(t, filepath.Join(RepoTemplatesDir(repo), "team.md"), smallTemplate)
	if err := os.MkdirAll(filepath.Join(home, TemplatesDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	// A colon is legal in a Unix file name.
	if err := os.WriteFile(filepath.Join(home, TemplatesDirName, "repo:team.md"), []byte(smallTemplate), 0o600); err != nil {
		t.Skipf("no colon in file names here: %v", err)
	}
	if got, ok := LookupTemplate(repo, "repo:team"); !ok || got.Source != SourceRepo {
		t.Errorf("repo:team = %+v, %v; a user file must not take the repo: name", got, ok)
	}

	for i := range maxRepoTemplates + 5 {
		writeFile(t, filepath.Join(RepoTemplatesDir(repo), fmt.Sprintf("z%03d.md", maxRepoTemplates+5-i)), smallTemplate)
	}
	names := RepoTemplateNames(repo)
	if len(names) != maxRepoTemplates || names[0] != "repo:team" || !slices.IsSorted(names) {
		t.Errorf("the cap kept %d names, first %q, sorted %v", len(names), names[0], slices.IsSorted(names))
	}
}
