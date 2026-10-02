package testsupport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// assets/brand/tokens.json is the one source for terva's colours. The web
// client generates its palette from it, and a vitest file snapshot holds that
// side. Two hand-written copies of the brand palette remain, and this file
// holds them: the palette table in assets/brand/README.md, and the :root
// block of the landing page. Before tokens.json, the brand palette lived in
// three places that nothing compared.

const brandTokensPath = "assets/brand/tokens.json"

type brandToken struct {
	Hex  string `json:"hex"`
	Name string `json:"name"`
}

// readBrandTokens returns the brand pigments by key. The JSON mixes a
// $comment string in with the entries, so each value decodes on its own.
func readBrandTokens(t *testing.T) map[string]brandToken {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, brandTokensPath))
	if err != nil {
		t.Fatalf("read %s: %v", brandTokensPath, err)
	}
	var doc struct {
		Brand map[string]json.RawMessage `json:"brand"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", brandTokensPath, err)
	}
	out := map[string]brandToken{}
	for key, raw := range doc.Brand {
		if key == "$comment" {
			continue
		}
		var tok brandToken
		if err := json.Unmarshal(raw, &tok); err != nil {
			t.Fatalf("%s: brand.%s: %v", brandTokensPath, key, err)
		}
		out[key] = tok
	}
	if len(out) < 10 {
		t.Fatalf("%s holds %d brand tokens; the parse is broken, not the palette", brandTokensPath, len(out))
	}
	return out
}

// A README palette row: | **Tar Black** | `#11100E` | use |
var readmePaletteRow = regexp.MustCompile("(?m)^\\|\\s*\\*\\*([^*]+)\\*\\*\\s*\\|\\s*`(#[0-9A-Fa-f]{6})`")

func TestBrandReadmePaletteMatchesTokens(t *testing.T) {
	tokens := readBrandTokens(t)
	b, err := os.ReadFile(filepath.Join(repoRoot, "assets/brand/README.md"))
	if err != nil {
		t.Fatalf("read assets/brand/README.md: %v", err)
	}
	rows := map[string]string{}
	for _, m := range readmePaletteRow.FindAllStringSubmatch(string(b), -1) {
		rows[m[1]] = m[2]
	}
	if len(rows) == 0 {
		t.Fatal("assets/brand/README.md has no palette rows; the table moved or the pattern is broken")
	}

	named := map[string]bool{}
	for key, tok := range tokens {
		if tok.Name == "" {
			continue
		}
		named[tok.Name] = true
		got, ok := rows[tok.Name]
		switch {
		case !ok:
			t.Errorf("README palette has no row for %s (brand.%s in %s)", tok.Name, key, brandTokensPath)
		case !strings.EqualFold(got, tok.Hex):
			t.Errorf("README lists %s as %s, but %s says %s", tok.Name, got, brandTokensPath, tok.Hex)
		}
	}
	for name := range rows {
		if !named[name] {
			t.Errorf("README palette lists %s, which %s does not name; add it there first", name, brandTokensPath)
		}
	}
}

// The landing page's palette: the first :root block, one --name: #hex per line.
var sitePaletteDecl = regexp.MustCompile(`(--[a-z0-9-]+):\s*(#[0-9A-Fa-f]{6})`)

// notOnSite lists the brand tokens the landing page does not declare, with the
// reason. Every other brand token must appear in the page's :root, so a page
// that drops a pigment fails here instead of passing with fewer to compare.
var notOnSite = map[string]string{
	"pine": "Pine Green is an optional secondary accent, and the page draws no element in it",
}

func TestSitePaletteMatchesTokens(t *testing.T) {
	requireSourceTree(t)
	tokens := readBrandTokens(t)
	src := readSitePage(t, repoRoot)

	start := strings.Index(src, ":root {")
	if start < 0 {
		t.Fatalf("%s has no :root block", sitePage)
	}
	block := src[start : start+strings.Index(src[start:], "}")]

	declared := map[string]bool{}
	for _, m := range sitePaletteDecl.FindAllStringSubmatch(block, -1) {
		key := strings.TrimPrefix(m[1], "--")
		tok, ok := tokens[key]
		if !ok {
			// The --tv-* terminal colours mirror packages/tui/theme.go, not the brand.
			continue
		}
		declared[key] = true
		if !strings.EqualFold(m[2], tok.Hex) {
			t.Errorf("%s declares %s: %s, but %s says brand.%s is %s", sitePage, m[1], m[2], brandTokensPath, key, tok.Hex)
		}
	}
	for key := range tokens {
		_, exempt := notOnSite[key]
		switch {
		case !declared[key] && !exempt:
			t.Errorf("%s does not declare --%s; declare it, or list it in notOnSite with the reason", sitePage, key)
		case declared[key] && exempt:
			t.Errorf("%s now declares --%s; remove it from notOnSite", sitePage, key)
		}
	}
	for key := range notOnSite {
		if _, ok := tokens[key]; !ok {
			t.Errorf("notOnSite lists %s, which %s no longer holds", key, brandTokensPath)
		}
	}
}
