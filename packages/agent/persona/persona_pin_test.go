package persona

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The builtin personas are go:embed'ed, so whatever line endings the CHECKOUT
// produced become the shipped prompt bytes. A Windows checkout converts to CRLF
// unless .gitattributes pins the path, and the charter tests match multi-line
// markers — so an unpinned directory fails them for reasons that name nothing
// about line endings ("missing the sub-agent deliverable contract paragraph").
//
// That pin is a PATH STRING, and this directory has moved once already: when the
// persona library became its own package the pin stayed pointing at
// packages/agent/build/personas, the seven review-crew charters came through a
// Windows runner as CRLF, and the failure surfaced on the release gate — the
// only Windows signal there is, since the internal forge has no Windows runner.
//
// So the string is joined to the directory the embed actually names. A rename
// that leaves the pin behind now fails here, on the commit that does it.
func TestTheEmbeddedPersonasArePinnedToLF(t *testing.T) {
	// The embed directive is the authority on which directory ships.
	src, err := os.ReadFile("persona.go")
	if err != nil {
		t.Fatalf("read persona.go: %v", err)
	}
	m := regexp.MustCompile(`//go:embed +(?:all:)?(\S+)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no //go:embed directive in persona.go — if the personas stopped being embedded, " +
			"delete this guard; if it moved, point the guard at it")
	}
	// Package-relative in the directive; repo-relative in .gitattributes.
	embedded := filepath.ToSlash(filepath.Join("packages/agent/persona", string(m[1])))

	attrs, err := os.ReadFile(filepath.Join("../../..", ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}

	var pinned []string
	for _, line := range strings.Split(string(attrs), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "eol=lf") {
			continue
		}
		pinned = append(pinned, strings.Fields(line)[0])
	}
	if len(pinned) == 0 {
		t.Fatal("parsed no eol=lf patterns from .gitattributes — the file moved or changed shape, and an " +
			"empty result would pass this check vacuously")
	}

	for _, pat := range pinned {
		// The patterns here are all "<dir>/**/*.md" shaped; the directory prefix
		// is what has to cover the embed, and matching that is enough to know a
		// CRLF checkout cannot reach these files.
		prefix := strings.TrimSuffix(pat, "/**/*.md")
		if prefix != pat && strings.HasPrefix(embedded+"/", prefix+"/") {
			return
		}
	}
	t.Errorf("persona.go embeds %s, which no .gitattributes eol=lf pattern covers:\n  %s\n"+
		"A CRLF checkout would poison the embedded charter bytes and the marker tests would fail on "+
		"Windows CI naming the marker, not the line endings. Update the pin to the directory the embed names.",
		embedded, strings.Join(pinned, "\n  "))
}
