package testsupport

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// A link into a heading that was renamed, moved or deleted lands the reader at
// the top of the page with no sign that anything is wrong, and nothing here
// noticed.
//
// scripts/release.sh check-links resolves the FILE a link names and drops the
// fragment on the floor: `target=${target%%#*}`. So a split that renames a
// heading breaks every inbound link to it and leaves the gate green. The
// instructive case from TKT-01M2JZ8W4 is docs/extensions.md pointing at
// cli.md#secrets-at-rest-terva-secret. It sat dead through three later pull
// requests, because the dangling link lives in a different file than the one
// being split — so reading the split page cannot find it — and cli.md still
// exists, so check-links cannot either.
//
// The scope is docs/**/*.md and README.md. It deliberately reaches further
// than check-links, which skips docs/plans/, docs/proposals/ and the rest of
// the record directories as link SOURCES because they are cut from the public
// tree. An archived plan is still read, and a fragment that goes nowhere
// misleads whoever reads it; there is no publication argument for leaving one
// in place. TKT-01M2NWKNB carries the repairs this guards.
//
// The ticket store is out of scope on purpose. A ticket body quotes example
// syntax — this one writes `](path#anchor)` in its own description — and a
// gate that reports a ticket for describing a link is a gate that gets
// switched off.
func TestDocumentAnchorsResolve(t *testing.T) {
	root := filepath.Join("..", "..")

	sources, err := anchorGateSources(root)
	if err != nil {
		t.Fatalf("collect markdown: %v", err)
	}
	if len(sources) < 40 {
		t.Fatalf("only %d markdown files in scope — the walk is broken, not the tree", len(sources))
	}

	slugs := map[string]map[string]bool{}
	type offender struct{ file, link, why string }
	var offenders []offender

	for _, src := range sources {
		body, err := os.ReadFile(filepath.Join(root, src))
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		dir := filepath.Dir(src)
		for _, link := range markdownLinks(string(body)) {
			path, anchor, ok := strings.Cut(link, "#")
			if !ok || anchor == "" {
				continue
			}
			switch {
			case strings.HasPrefix(link, "http://"), strings.HasPrefix(link, "https://"), strings.HasPrefix(link, "mailto:"):
				continue
			}
			target := src
			if path != "" {
				target = filepath.Clean(filepath.Join(dir, path))
			}
			// Only a markdown file offers headings to link into. A fragment on
			// anything else (a line number on a source file, a query on a
			// generated page) is not this gate's business.
			if !strings.HasSuffix(target, ".md") {
				continue
			}
			if _, done := slugs[target]; !done {
				body, err := os.ReadFile(filepath.Join(root, target))
				if err != nil {
					slugs[target] = nil
				} else {
					slugs[target] = headingSlugs(string(body))
				}
			}
			switch set := slugs[target]; {
			case set == nil:
				offenders = append(offenders, offender{src, link, "the file does not exist"})
			case !set[strings.ToLower(anchor)]:
				offenders = append(offenders, offender{src, link, "no heading in " + target + " makes that anchor"})
			}
		}
	}

	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i].file != offenders[j].file {
			return offenders[i].file < offenders[j].file
		}
		return offenders[i].link < offenders[j].link
	})
	for _, o := range offenders {
		t.Errorf("%s: link to %s — %s", o.file, o.link, o.why)
	}
	if len(offenders) > 0 {
		t.Logf("A heading that was renamed keeps its section: read the target page " +
			"and point at the heading that carries the content now. Retitling the " +
			"link text to match is usually part of the repair, because the old " +
			"fragment is often quoted in it.")
	}
}

// anchorGateSources lists README.md and every markdown file under docs/.
func anchorGateSources(root string) ([]string, error) {
	out := []string{"README.md"}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// markdownLinkRe matches the target of an inline link. A space ends it, so the
// optional title in `](path "Title")` is not mistaken for part of the path.
var markdownLinkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// htmlCommentRe matches a comment inside a heading. The repository writes
// `<!-- rename:keep -->` on lines a rename sweep must not touch, and a renderer
// derives no anchor text from it.
var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// markdownLinks returns every inline link target outside fenced code.
//
// The fence test matters in both directions and this is the half that is easy
// to forget: a sample link in a code block is text to read, not a link to
// follow, and reporting one teaches the reader to ignore this gate.
func markdownLinks(src string) []string {
	var out []string
	eachProseLine(src, func(line string) {
		for _, m := range markdownLinkRe.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	})
	return out
}

// headingSlugs returns the anchors a page offers, as a renderer derives them.
//
// Duplicate heading text gets the "-1", "-2" suffix GitHub and Forgejo both
// append, so a page with two "Notes" sections offers `notes` and `notes-1`.
func headingSlugs(src string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	eachProseLine(src, func(line string) {
		text, ok := headingText(line)
		if !ok {
			return
		}
		s := slugify(text)
		if s == "" {
			return
		}
		if n := seen[s]; n > 0 {
			out[s+"-"+strconv.Itoa(n)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	})
	return out
}

// headingText returns the text of an ATX heading, or false for any other line.
func headingText(line string) (string, bool) {
	hashes := 0
	for hashes < len(line) && line[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 || hashes == len(line) || line[hashes] != ' ' {
		return "", false
	}
	return strings.TrimSpace(line[hashes+1:]), true
}

// slugify derives the fragment a renderer gives a heading: lower case, every
// character outside letters, digits, spaces, hyphens and underscores removed,
// then each space turned into a hyphen.
//
// "Each space" is the part worth stating, because getting it wrong looks
// right. A run of spaces does NOT collapse, and this corpus is full of
// headings that produce one: "Carriers — current state" loses the em dash and
// keeps the two spaces around it, so the anchor is `carriers--current-state`
// with two hyphens. A slugger that collapses whitespace reports eight healthy
// links in docs/ as broken.
func slugify(s string) string {
	s = htmlCommentRe.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// eachProseLine calls fn for every line outside a fenced code block.
//
// A fence opens and closes with three backticks or three tildes at the start of
// the line, which is all this corpus uses. Tracking them is what keeps a "#"
// in a shell sample from registering as a heading.
func eachProseLine(src string, fn func(line string)) {
	fenced := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		fn(line)
	}
}
