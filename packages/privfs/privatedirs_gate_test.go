package privfs_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// mkdirModeRe matches an os.MkdirAll whose mode argument is a literal, and
// captures the literal. A computed mode (a variable, privfs.DirMode) does not
// match and is not the target: this gate is about the 0o755 typed by hand.
var mkdirModeRe = regexp.MustCompile(`os\.MkdirAll\([^,]+,\s*(0o?[0-7]{3,4})\s*\)`)

// TestPrivateTreesAreOwnerOnly enforces that everything writing under
// $TERVA_HOME creates its directories owner-only.
//
// The bug this exists to stop was real and quiet: the host created
// ext-data/<name> at 0700 through privfs, and then the extension SDK's own
// helpers created SUBdirectories inside it at 0755 — as did two connector
// binaries around the state dir that holds a bot token. Nothing failed,
// because the 0700 parent was doing the work; the modes were wrong in a way
// that only mattered once someone moved, copied, or repaired the tree. And
// privfs.RepairOnce cannot save it: that walk is gated on a marker file and
// runs once per install, long before these directories exist.
//
// The gate is deliberately EMPTY of subject names — it scans every non-test
// Go file in the packages that write under the data home and reports any
// hand-typed mode that grants group or other any bit. A new writer enrolls
// itself the moment it is added, which is the only kind of audit that stays
// true. os.WriteFile is left alone on purpose: an extension staging a helper
// binary legitimately wants 0755 on the FILE, and privfs.WriteFile exists for
// when it does not.
func TestPrivateTreesAreOwnerOnly(t *testing.T) {
	root := filepath.Join("..", "..")
	// Every tree that writes under $TERVA_HOME. Not a list of offenders — a
	// list of places where the rule applies.
	scan := []string{
		filepath.Join("packages", "agent", "ext"),
		filepath.Join("packages", "agent", "extdriver"),
		filepath.Join("packages", "agent", "connsdk"),
		filepath.Join("packages", "agent", "chat"),
		filepath.Join("packages", "agent", "config"),
		filepath.Join("packages", "provider", "auth"),
		filepath.Join("cmd", "terva-discord-connector"),
		filepath.Join("cmd", "terva-telegram-connector"),
		filepath.Join("cmd", "terva-mcp-bridge"),
	}

	// The only legitimate group-readable directories in those trees, each with
	// its reason. A PROJECT directory is a repo file: `.terva/` is committed,
	// shared, and read by everyone who checks the repository out, so making it
	// owner-only would be actively wrong. Nothing secret may live there —
	// ProjectConfig's type is the guard for that (see packages/agent/config).
	exempt := map[string]string{
		filepath.Join("packages", "agent", "config", "extmcp.go"): "writes the PROJECT .terva/config.json, a committed repo file, not $TERVA_HOME",
	}

	var offenders []string
	for _, sub := range scan {
		dir := filepath.Join(root, sub)
		if _, err := os.Stat(dir); err != nil {
			continue // a tree that has moved is not a failure of this gate
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if testsupport.SkipScanDir(root, path, d) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if _, ok := exempt[rel]; ok {
				return nil
			}
			for i, line := range strings.Split(string(data), "\n") {
				code := line
				if idx := strings.Index(code, "//"); idx >= 0 {
					code = code[:idx]
				}
				m := mkdirModeRe.FindStringSubmatch(code)
				if m == nil {
					continue
				}
				mode, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(m[1], "0o"), "0"), 8, 32)
				if err != nil {
					continue
				}
				if mode&0o077 != 0 {
					offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("group/other-accessible directory under $TERVA_HOME — use privfs.MkdirAll:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
