package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// Every tool that takes a path authorizes it one of two ways, and picking the
// wrong one is not a style question.
//
//   - CheckPathRead is for a READ SOURCE. It consults secretRoots, secretNames,
//     secretExceptions and guardedRoots, so it refuses $TERVA_HOME/auth.json,
//     the session transcripts and the logs — wherever they sit.
//   - CheckPath is for a WRITE TARGET. It routes to checkUnder, a pure
//     containment test against Root that knows nothing about secrets.
//
// chat_send_file and chat_send_image called CheckPath on a read source. With
// cwd = $HOME the sandbox Root is $HOME, TERVA_HOME defaults to $HOME/.terva,
// containment passed, and the model could upload auth.json to a chat room —
// while `read`, `bash cat` and share_file all refused the same path. Under
// --project, TERVA_HOME is beneath cwd unconditionally, so it held for any cwd.
//
// The registry is scanned rather than listed: a tool added tomorrow enrolls
// itself. readSourceTools names the tools whose path argument is a read source;
// anything else is expected to use the containment check.
var readSourceTools = map[string]bool{
	"read.go":            true,
	"grep.go":            true,
	"glob.go":            true,
	"share_file.go":      true,
	"chat_send.go":       true,
	"session_inspect.go": true,
}

var (
	checkPathRe     = regexp.MustCompile(`Sandbox\.CheckPath\(`)
	checkPathReadRe = regexp.MustCompile(`Sandbox\.CheckPathRead\(`)
)

func TestReadSourceToolsUseTheSecretAwareCheck(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	var wrong []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		readN := len(checkPathReadRe.FindAll(src, -1))
		// CheckPathRead also matches CheckPath's prefix, so subtract.
		plainN := len(checkPathRe.FindAll(src, -1)) - readN
		if readN == 0 && plainN == 0 {
			continue
		}
		scanned++
		if readSourceTools[name] && plainN > 0 {
			wrong = append(wrong, name+": a read source authorized with Sandbox.CheckPath, "+
				"which is containment-only and never consults the secret deny list")
		}
		if !readSourceTools[name] && readN > 0 {
			wrong = append(wrong, name+": uses Sandbox.CheckPathRead but is not listed as a read-source tool — "+
				"add it to readSourceTools, or use CheckPath if its path is a write target")
		}
	}
	if scanned < 5 {
		t.Fatalf("only %d files carry a sandbox path check; the scan is broken and this census proves nothing", scanned)
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("path authorization does not match the tool's direction:\n  %s", strings.Join(wrong, "\n  "))
	}
}

// The behavioural half. The census above reads source; this proves the two
// checks actually DIFFER on the file that matters, so a refactor collapsing
// them into one cannot pass the census quietly.
//
// The secret roots are registered explicitly, as build/sandbox_roots.go does at
// startup: NewSandbox populates none of them (set-once-at-setup contract), so a
// bare sandbox would allow everything and this test would pass vacuously.
func TestTheTwoPathChecksDisagreeOnACredentialFile(t *testing.T) {
	root := testsupport.TempDir(t)
	home := filepath.Join(root, ".terva")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(home, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"k":"v"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sb := NewSandbox(root)
	sb.AddSecretRoot(auth)
	sb.Lock()

	// The read check must refuse it: this is a credential.
	if err := sb.CheckPathRead(auth); err == nil {
		t.Error("CheckPathRead allowed a registered secret root — the deny list is not being consulted, " +
			"so read / share_file / chat_send would hand over provider credentials")
	}
	// The write check must ALLOW it, because it only tests containment and
	// auth.json is under Root. That asymmetry is the entire reason the two
	// checks exist, and it is what made chat_send's wrong choice exploitable.
	if err := sb.CheckPath(auth); err != nil {
		t.Errorf("CheckPath refused a path inside Root (%v) — if containment now also consults the "+
			"secret list the two checks have merged, and the census above no longer distinguishes anything", err)
	}
}
