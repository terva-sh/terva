package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateOnePersona(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"valid.md", "---\nname: Ok\naccent_color: \"#7aa2f7\"\n---\nA real charter.", true},
		{"noname.md", "---\nsummary: x\n---\nBody.", false},
		{"macro.md", "---\nname: Ok\n---\nHello {{user}}.", false},
		{"empty.md", "---\nname: Ok\n---\n", false},
		{"badcolor.md", "---\nname: Ok\naccent_color: blue\n---\nCharter.", false},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := validateOnePersona(p); got != c.want {
			t.Errorf("%s: validateOnePersona=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestPersonaClip(t *testing.T) {
	if got := personaClip("hello", 10); got != "hello" {
		t.Errorf("no-clip: %q", got)
	}
	if got := personaClip("hello world", 5); got != "hell…" {
		t.Errorf("clip: %q", got)
	}
}
