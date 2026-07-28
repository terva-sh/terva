package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeExtension lays down a minimal installed extension bundle under
// home/extensions/<name>.
func writeExtension(t *testing.T, home, name string, manifest map[string]any, skillBodies map[string]string) string {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if manifest["name"] == nil {
		manifest["name"] = name
	}
	if manifest["exec"] == nil {
		manifest["exec"] = "./noop"
	}
	mb, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	for skillName, body := range skillBodies {
		sd := filepath.Join(dir, "skills", skillName)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		md := "---\nname: " + skillName + "\ndescription: from bundle\n---\n" + body
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
