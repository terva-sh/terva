package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

func TestSensitiveReadsAcrossJailStates(t *testing.T) {
	for _, state := range []string{"locked", "unlocked", "initially-unjailed"} {
		t.Run(state, func(t *testing.T) {
			root := testsupport.TempDir(t)
			cwd := testsupport.TempDir(t)
			denied := []string{"auth.json", "logs/bot.log", "component/secrets.key", "component/private.txt"}
			allowed := []string{"public.txt", "logs/ext-example.log", "component/public.txt"}
			for _, rel := range append(append([]string{}, denied...), allowed...) {
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("synthetic-match "+rel+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			sb := NewSandbox(cwd)
			sb.AddSecretRoot(filepath.Join(root, "auth.json"), filepath.Join(root, "logs"))
			sb.AddSecretException(filepath.Join(root, "logs"), "ext-*.log")
			sb.AddSecretNameUnder(root, "secrets.key")
			sb.AddGuardedRoot(filepath.Join(root, "component"), func(rel string) (bool, string) {
				return rel != "private.txt", "synthetic private state"
			})
			if state != "initially-unjailed" {
				sb.Lock()
			}
			if state == "unlocked" {
				sb.Unlock()
			}
			read := &ReadTool{CWD: cwd, Sandbox: sb}
			grep := &GrepTool{CWD: cwd, Sandbox: sb}
			glob := &GlobTool{CWD: cwd, Sandbox: sb}
			pub := &stubPublisher{}
			share := &ShareFileTool{CWD: cwd, Sandbox: sb, Publisher: pub}
			for _, rel := range denied {
				path := filepath.Join(root, rel)
				args := mustJSON(t, map[string]any{"path": path, "pattern": "synthetic-match"})
				if _, err := read.Execute(context.Background(), args, nil); err == nil {
					t.Errorf("read allowed %s", rel)
				}
				if _, err := grep.Execute(context.Background(), args, nil); err == nil {
					t.Errorf("grep allowed direct read of %s", rel)
				}
				if _, err := share.Execute(context.Background(), args, nil); err == nil {
					t.Errorf("share_file allowed %s", rel)
				}
				if err := sb.CheckCommand("cat '" + filepath.ToSlash(path) + "'"); err == nil {
					t.Errorf("bash allowed %s", rel)
				}
			}
			if len(pub.calls) != 0 {
				t.Fatal("publisher received a denied file")
			}
			for _, includeIgnored := range []bool{false, true} {
				for _, tool := range []struct {
					core.Tool
					pattern string
				}{{grep, "synthetic-match"}, {glob, "**"}} {
					args := mustJSON(t, map[string]any{"path": root, "pattern": tool.pattern, "include_ignored": includeIgnored})
					res, err := tool.Execute(context.Background(), args, nil)
					if err != nil {
						t.Fatalf("%s parent search: %v", tool.Name(), err)
					}
					out := resultText(res)
					for _, rel := range denied {
						if strings.Contains(out, rel) {
							t.Errorf("%s parent search exposed %s: %s", tool.Name(), rel, out)
						}
					}
					for _, rel := range allowed {
						if !strings.Contains(out, rel) {
							t.Errorf("%s parent search hid allowed %s: %s", tool.Name(), rel, out)
						}
					}
				}
			}
			for _, rel := range allowed {
				path := filepath.Join(root, rel)
				if err := sb.CheckPathRead(path); err != nil {
					t.Errorf("allowed read %s: %v", rel, err)
				}
				if err := sb.CheckCommand("cat '" + filepath.ToSlash(path) + "'"); err != nil {
					t.Errorf("allowed shell read %s: %v", rel, err)
				}
			}
			if err := sb.CheckPath(filepath.Join(root, "public.txt")); (err != nil) != (state == "locked") {
				t.Errorf("write confinement changed: %v", err)
			}
			if err := sb.CheckCommand("sudo true"); (err != nil) != (state == "locked") {
				t.Errorf("command confinement changed: %v", err)
			}
		})
	}
}

func TestSensitiveReadSymlinkTarget(t *testing.T) {
	root := testsupport.TempDir(t)
	secret := filepath.Join(root, "auth.json")
	if err := os.WriteFile(secret, []byte("synthetic secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.txt")
	if err := os.Symlink(secret, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sb := NewSandbox(root)
	sb.AddSecretRoot(secret)
	sb.AddSecretException(root, "alias.txt")
	for _, locked := range []bool{true, false} {
		if locked {
			sb.Lock()
		} else {
			sb.Unlock()
		}
		if err := sb.CheckPathRead(alias); err == nil {
			t.Errorf("alias bypassed secret target rule (locked=%v)", locked)
		}
	}
}
