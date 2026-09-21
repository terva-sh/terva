package testsupport

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The terva.sh landing page is hand-written HTML under docs/vanity/site, an
// excluded tree that no other gate reads: check-links walks only Markdown and
// skips docs/vanity, and terva-ste-lint collects only the shipped .md tier. So
// the page drifted for two months and made six false claims before anyone
// looked (TKT-01M2XRJ6J7).
//
// A sentence on the page is one of two kinds. A pitch is a judgment and stays
// with the writer. A fact is a sentence that becomes false when the tree
// changes without it, and every one of the six was a fact: a count, a
// platform, a list. Facts are gateable because the tree already holds the
// answer, so the writer marks each one with a data-fact attribute and this
// test checks it against its source. Unmarked prose is out of scope by
// definition, which is what keeps the gate credible: it never fires on a
// pitch. The rest of the test is the same shape as the pointer gate beside it:
// links resolve, assets exist and are used, brand copies match their masters,
// and the em-dash ban (AGENTS.md) reaches the HTML.
//
// The page is not parsed as a DOM. There is no HTML parser in go.mod, and one
// hand-written page does not justify a dependency for a tag-stripping regexp.

const sitePage = "docs/vanity/site/index.html"

func TestSitePageFacts(t *testing.T) {
	root := filepath.Join("..", "..")
	src := readSitePage(t, root)

	facts := map[string]string{}
	for _, m := range siteFactRe.FindAllStringSubmatch(src, -1) {
		facts[m[1]] = strings.TrimSpace(stripTags(m[2]))
	}
	if len(facts) == 0 {
		t.Fatalf("%s carries no data-fact spans; every checkable claim on the page should be marked", sitePage)
	}

	// Each check returns "" when the fact holds, or the correction.
	checks := map[string]func(text string) string{
		"persona-count": func(text string) string {
			want := countBuiltinPersonas(t, root)
			if n, ok := numberIn(text); !ok || n != want {
				return "the tree ships " + strconv.Itoa(want) + " built-in personas"
			}
			return ""
		},
		"provider-count": func(text string) string {
			// "25+" is a lower bound and stays true as the list grows, so a
			// plus-claim may only be checked for overclaiming. A bare number
			// is exact and must match.
			want := countProviderLabels(t, root)
			n, ok := numberIn(text)
			switch {
			case !ok:
				return "no number found; the tree names " + strconv.Itoa(want) + " providers"
			case n > want:
				return "claims more than the " + strconv.Itoa(want) + " providers the tree names"
			case !strings.Contains(text, "+") && n != want:
				return "the tree names " + strconv.Itoa(want) + " providers; write that, or write a lower bound with +"
			}
			return ""
		},
		"release-platforms": func(text string) string {
			want := releasePlatformSentence(t, root)
			if text != want {
				return "the goreleaser matrix reads: " + want
			}
			return ""
		},
	}

	names := make([]string, 0, len(facts))
	for n := range facts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		check, ok := checks[name]
		if !ok {
			t.Errorf("%s: data-fact=%q has no check in this test; add one or unmark it", sitePage, name)
			continue
		}
		if msg := check(facts[name]); msg != "" {
			t.Errorf("%s: data-fact=%q says %q, but %s", sitePage, name, facts[name], msg)
		}
	}
	for name := range checks {
		if _, ok := facts[name]; !ok {
			t.Errorf("%s: no data-fact=%q span; the page must mark that claim so it is checked", sitePage, name)
		}
	}
}

func TestSitePageLinksResolve(t *testing.T) {
	root := filepath.Join("..", "..")
	src := readSitePage(t, root)

	shipped := map[string]bool{}
	for _, d := range docsThatShip {
		shipped[d] = true
	}
	seen := map[string]bool{}
	for _, m := range siteDocLinkRe.FindAllStringSubmatch(src, -1) {
		path, frag := m[1], m[2]
		if seen[path+frag] {
			continue
		}
		seen[path+frag] = true
		if !shipped[path] {
			t.Errorf("%s links %s, which is not in docsThatShip and so is not on the release branch", sitePage, path)
			continue
		}
		if frag != "" && !markdownHasAnchor(t, filepath.Join(root, path), frag) {
			t.Errorf("%s links %s#%s, and that heading does not exist", sitePage, path, frag)
		}
	}
	// Internal anchors: every href="#x" needs an id="x".
	ids := map[string]bool{}
	for _, m := range siteIDRe.FindAllStringSubmatch(src, -1) {
		ids[m[1]] = true
	}
	for _, m := range siteHashLinkRe.FindAllStringSubmatch(src, -1) {
		if !ids[m[1]] {
			t.Errorf("%s links #%s, and no element carries that id", sitePage, m[1])
		}
	}
}

func TestSitePageAssets(t *testing.T) {
	root := filepath.Join("..", "..")
	site := filepath.Join(root, "docs/vanity/site")

	// Every reference from the three HTML files and the manifest.
	referenced := map[string]bool{}
	for _, f := range []string{"index.html", "404.html", "terva/index.html", "site.webmanifest"} {
		b, err := os.ReadFile(filepath.Join(site, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range siteAssetRefRe.FindAllStringSubmatch(string(b), -1) {
			ref := strings.TrimPrefix(m[1], "/")
			referenced[ref] = true
			if _, err := os.Stat(filepath.Join(site, ref)); err != nil {
				t.Errorf("docs/vanity/site/%s references %s, which does not exist", f, ref)
			}
		}
	}

	// Every file under assets/ and every root icon is referenced by something,
	// and every one with a brand master is byte-identical to it.
	var files []string
	entries, err := os.ReadDir(filepath.Join(site, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		files = append(files, "assets/"+e.Name())
	}
	files = append(files, "favicon.ico", "favicon.svg", "apple-touch-icon.png",
		"android-chrome-192x192.png", "android-chrome-512x512.png")
	for _, f := range files {
		if !referenced[f] {
			t.Errorf("docs/vanity/site/%s is referenced by no page or manifest; delete it or use it", f)
		}
		if master := brandMaster(root, filepath.Base(f)); master != "" {
			a, _ := os.ReadFile(filepath.Join(site, f))
			b, _ := os.ReadFile(master)
			if !bytes.Equal(a, b) {
				rel, _ := filepath.Rel(root, master)
				t.Errorf("docs/vanity/site/%s differs from its master %s; re-copy it", f, rel)
			}
		}
	}
}

// The em-dash ban applies to docs/, and the maintainer's ruling of 2026-09-19
// is that it reaches this page, script and style included: the demo clip
// strings are prose a visitor reads. Sized like the docs gate, at zero.
func TestSitePageHasNoEmDash(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{"index.html", "404.html", "terva/index.html"}
	// A cast is prose a visitor reads, frame by frame, so it is held to the
	// same rule; the vendored player is not ours to edit and is skipped.
	casts, _ := filepath.Glob(filepath.Join(root, "docs/vanity/site/assets/*.cast"))
	for _, c := range casts {
		rel, _ := filepath.Rel(filepath.Join(root, "docs/vanity/site"), c)
		files = append(files, rel)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, "docs/vanity/site", f))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "—") {
				t.Errorf("docs/vanity/site/%s:%d: em dash", f, i+1)
			}
		}
	}
}

// --- helpers -------------------------------------------------------------

var (
	// <span data-fact="name">text</span>; the text may hold inline tags.
	siteFactRe = regexp.MustCompile(`(?s)<[a-z]+[^>]*\sdata-fact="([a-z-]+)"[^>]*>(.*?)</[a-z]+>`)
	// href="https://github.com/terva-sh/terva/blob/release/docs/<page>.md#<heading>"
	siteDocLinkRe  = regexp.MustCompile(`href="https://github\.com/terva-sh/terva/blob/release/(docs/[A-Za-z0-9_./-]+\.md)(?:#([A-Za-z0-9_-]+))?"`)
	siteIDRe       = regexp.MustCompile(`\sid="([A-Za-z0-9_-]+)"`)
	siteHashLinkRe = regexp.MustCompile(`href="#([A-Za-z0-9_-]+)"`)
	// src/href/content attributes naming a site-local file, in HTML or in the
	// manifest's JSON, by relative path or by the site's own absolute URL. A
	// cast and the vendored player count as assets too (TKT-01M2Y0YQ).
	siteAssetRefRe  = regexp.MustCompile(`(?:src|href|content)"?\s*[:=]\s*"(?:https://terva\.sh)?/?((?:assets/)?[A-Za-z0-9_.-]+\.(?:png|svg|webp|ico|jpg|cast|js|css))"`)
	tagRe           = regexp.MustCompile(`<[^>]+>`)
	siteNumberWords = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20}
)

func readSitePage(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, sitePage))
	if err != nil {
		t.Fatalf("read %s: %v", sitePage, err)
	}
	return string(b)
}

func stripTags(s string) string { return tagRe.ReplaceAllString(s, "") }

// numberIn finds the first number in a marked fact, as digits ("25+", "16")
// or as a word ("twelve").
func numberIn(text string) (int, bool) {
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if n, err := strconv.Atoi(w); err == nil {
			return n, true
		}
		if n, ok := siteNumberWords[w]; ok {
			return n, true
		}
	}
	return 0, false
}

// countBuiltinPersonas counts what packages/agent/persona embeds: every .md
// under personas/builtin that is not a crew README.
func countBuiltinPersonas(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(root, "packages/agent/persona/personas/builtin"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipScanDir(root, p, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") && d.Name() != "README.md" {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// countProviderLabels counts the case clauses of the provider label switch in
// packages/provider/labels.go, the one place every provider id is named so a
// screen can show it. Read through go/ast so a comment or a string elsewhere
// in the file cannot be counted.
func countProviderLabels(t *testing.T, root string) int {
	t.Helper()
	path := filepath.Join(root, "packages/provider/labels.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	best := 0
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		count := 0
		for _, c := range sw.Body.List {
			if cc, ok := c.(*ast.CaseClause); ok && cc.List != nil {
				count += len(cc.List)
			}
		}
		if count > best {
			best = count
		}
		return true
	})
	if best == 0 {
		t.Fatalf("%s: no switch over provider ids found", path)
	}
	return best
}

// releasePlatformSentence renders the goreleaser build matrix as the one
// sentence the page must carry, so a change to the matrix prints the new
// sentence in the failure.
func releasePlatformSentence(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
			Ignore []struct {
				Goos   string `yaml:"goos"`
				Goarch string `yaml:"goarch"`
			} `yaml:"ignore"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Builds) == 0 {
		t.Fatal(".goreleaser.yaml: no builds")
	}
	bld := cfg.Builds[0]
	osName := map[string]string{"linux": "Linux", "darwin": "macOS", "windows": "Windows"}
	ignored := map[string]bool{}
	for _, ig := range bld.Ignore {
		ignored[ig.Goos+"/"+ig.Goarch] = true
	}
	var full, partial []string
	for _, os := range bld.Goos {
		var archs []string
		for _, arch := range bld.Goarch {
			if !ignored[os+"/"+arch] {
				archs = append(archs, arch)
			}
		}
		name := osName[os]
		if name == "" {
			name = os
		}
		if len(archs) == len(bld.Goarch) {
			full = append(full, name)
		} else {
			partial = append(partial, name+" on "+strings.Join(archs, " and "))
		}
	}
	s := strings.Join(full, " and ") + " on " + strings.Join(bld.Goarch, " and ")
	if len(partial) > 0 {
		s += ", and " + strings.Join(partial, ", ")
	}
	return s
}

// markdownHasAnchor reports whether a heading in the file slugs to frag, using
// the same rule as the docs anchor gate.
func markdownHasAnchor(t *testing.T, path, frag string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return headingSlugs(string(b))[frag]
}

// brandMaster mirrors scripts/site.sh's brand_master: the source under
// assets/brand, then a generated size under assets/brand/exports, then a
// documentation capture under assets/captures (decision 0020).
func brandMaster(root, name string) string {
	for _, c := range []string{"assets/brand/" + name, "assets/brand/exports/" + name, "assets/captures/" + name} {
		if _, err := os.Stat(filepath.Join(root, c)); err == nil {
			return filepath.Join(root, c)
		}
	}
	return ""
}

// The fact checks are only as good as their readers, so pin them: the number
// reader on the shapes the page uses, and the platform sentence on the matrix
// as it stands, so a change to .goreleaser.yaml shows up here as the new
// sentence the page must carry.
func TestSiteGateHelpers(t *testing.T) {
	root := filepath.Join("..", "..")
	for text, want := range map[string]int{"25+": 25, "sixteen personas": 16, "twelve": 12, "no number here": 0} {
		n, ok := numberIn(text)
		if (want == 0) == ok || n != want {
			t.Errorf("numberIn(%q) = %d, %v; want %d", text, n, ok, want)
		}
	}
	if got := releasePlatformSentence(t, root); got != "Linux and macOS on amd64 and arm64, and Windows on amd64" {
		t.Errorf("platform sentence: %q", got)
	}
	if n := countBuiltinPersonas(t, root); n < 10 {
		t.Errorf("built-in personas: %d, which is fewer than the crews alone", n)
	}
	if n := countProviderLabels(t, root); n < 20 {
		t.Errorf("provider labels: %d, which is fewer than docs/models.md lists", n)
	}
}
