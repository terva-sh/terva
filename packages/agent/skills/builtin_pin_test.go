package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The builtin skills are go:embed'ed, so the checkout's line endings become the
// shipped bytes, and an always-on skill's whole text goes into the system
// prompt. A Windows checkout converts to CRLF unless .gitattributes pins the
// path. The skills were unpinned until the public CI's Windows job failed the
// prompt goldens twice at a release gate, with output that printed identical to
// the golden. That job is the only Windows signal there is.
//
// persona_pin_test.go guards the persona embed the same way: the pin is a path
// string, so it is joined to the directory the embed actually names, and a
// move that leaves the pin behind fails here on Linux too.
func TestTheEmbeddedSkillsArePinnedToLF(t *testing.T) {
	src, err := os.ReadFile("builtin.go")
	if err != nil {
		t.Fatalf("read builtin.go: %v", err)
	}
	m := regexp.MustCompile(`//go:embed +(?:all:)?(\S+)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no //go:embed directive in builtin.go. If the builtin skills stopped being embedded, " +
			"delete this guard; if they moved, point the guard at them")
	}
	embedded := filepath.ToSlash(filepath.Join("packages/agent/skills", string(m[1])))

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
		t.Fatal("parsed no eol=lf patterns from .gitattributes. The file moved or changed shape, and an " +
			"empty result would pass this check vacuously")
	}
	for _, pat := range pinned {
		prefix := strings.TrimSuffix(pat, "/**/*.md")
		if prefix != pat && strings.HasPrefix(embedded+"/", prefix+"/") {
			return
		}
	}
	t.Errorf("builtin.go embeds %s, which no .gitattributes eol=lf pattern covers:\n  %s\n"+
		"A CRLF checkout would put carriage returns into the system prompt, and the prompt goldens "+
		"would fail on Windows with output that prints identical to the golden.",
		embedded, strings.Join(pinned, "\n  "))
}

// The bytes themselves, which is what a Windows run can see and the pin check
// cannot: a checkout that ignored the pin still fails here, naming the file.
func TestTheEmbeddedSkillsHoldNoCarriageReturn(t *testing.T) {
	var n int
	err := fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		if strings.Contains(string(data), "\r") {
			t.Errorf("%s holds a carriage return: the checkout converted it to CRLF, and the pin in "+
				".gitattributes did not hold", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("read no embedded skill files, so the check above proved nothing")
	}
}
