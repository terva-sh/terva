package lineframe

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// This package's doc calls itself "the one bounded line reader for terva's
// newline-delimited wire protocols", docs/resource-limits.md says "do not
// reintroduce a raw Scanner" on a peer-facing reader and asserts "All
// newline-delimited JSON wire protocols read through lineframe.Reader", and
// docs/architecture repeats the claim. Three statements of a rule, and nothing
// that could fail when it was broken — so it was, in nine places.
//
// The gate is DEFAULT-DENY. Every raw line reader in the tree must be named
// here with a reason, so a new one fails until someone decides which it is: a
// wire peer (use lineframe) or a local trusted input (add it below, with why).
// The polarity matters — a list of known OFFENDERS cannot fail when an offender
// is added, which is the whole lesson of host_census_test.go.
//
// Scope is this package's own: WIRE peers only. A local file has no adversary
// and no framing contract, and capping it would break the exports and
// transcripts terva is required to read whole.
var localReaders = map[string]string{
	// Local trusted FILES. lineframe's package doc excludes these by name.
	"packages/core/session_portable.go":       "session transcripts and JSONL exports: local files, deliberately uncapped — see this file's own ReadBytes comments",
	"packages/core/session.go":                "replay of terva's own session file",
	"packages/agent/swarm/event.go":           "replay of the durable swarm event log, a file terva wrote",
	"packages/agent/build/usageledger.go":     "the local usage ledger file",
	"packages/agent/workflow/runs/journal.go": "the local workflow run journal",
	"cmd/terva-ste-lint/dictionary.go":        "the checked-in approved-words dictionary",

	// The operator's terminal. Not a peer: there is no framing contract, no
	// adversary, and a human cannot type a gigabyte without a newline.
	"packages/agent/cli.go":                    "interactive session picker on stdin",
	"packages/agent/migratecmd.go":             "interactive y/n prompt on stdin",
	"packages/agent/modes/discord/service.go":  "interactive `bot setup` token prompt on stdin",
	"packages/agent/modes/telegram/service.go": "interactive `bot setup` token prompt on stdin",
	"cmd/terva-discord-connector/main.go":      "interactive `setup` token prompt on stdin",
	"cmd/terva-telegram-connector/main.go":     "interactive `setup` token prompt on stdin",

	// Teaching code. An example is copied into a reader's own project, where
	// terva.sh/terva/packages/lineframe is not on their import path; the cost of
	// the raw reader there is a broken demo, not a lost session.
	"examples/rpc/go/main.go": "standalone example a reader copies out of this module",
}

var rawLineReaders = regexp.MustCompile(`bufio\.NewScanner\(|ReadBytes\('\\n'\)|ReadString\('\\n'\)`)

// scanForRawLineReaders returns "path:line" for every raw line reader under
// root, keyed by repo-relative path, plus how many files were examined.
func scanForRawLineReaders(root string) (map[string][]string, int, error) {
	found := map[string][]string{}
	var scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(root, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			// testdata holds deliberately minimal stub binaries; they are
			// fixtures, not shipped readers.
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// This package is the implementation; it is bufio's one legitimate home.
		if strings.HasPrefix(rel, "packages/lineframe/") {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if rawLineReaders.MatchString(line) {
				found[rel] = append(found[rel], rel+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	return found, scanned, err
}

func TestEveryWireReaderGoesThroughLineframe(t *testing.T) {
	const root = "../.."
	found, scanned, err := scanForRawLineReaders(root)
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("only %d production Go files were scanned; the walk is broken and this gate proves nothing", scanned)
	}
	var offenders []string
	for rel, hits := range found {
		if _, ok := localReaders[rel]; ok {
			continue
		}
		offenders = append(offenders, hits...)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("raw line readers outside the declared local-input set. A wire peer must read through "+
			"lineframe — Reader to RECOVER (multiplexed carriers) or ReadFrame to REJECT (one logical "+
			"payload). If this really is a local file or the operator's terminal, add it to localReaders "+
			"with the reason:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// An exemption ledger rots the moment a listed file stops matching: the entry
// then documents a rule nobody is following and hides the next real offender in
// that file behind a stale justification.
func TestEveryDeclaredLocalReaderStillHasOne(t *testing.T) {
	const root = "../.."
	found, _, err := scanForRawLineReaders(root)
	if err != nil {
		t.Fatal(err)
	}
	var stale []string
	for rel, why := range localReaders {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			stale = append(stale, rel+": the file no longer exists ("+why+")")
			continue
		}
		if len(found[rel]) == 0 {
			stale = append(stale, rel+": no raw line reader left here; drop the entry ("+why+")")
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("localReaders has entries that no longer describe anything:\n  %s", strings.Join(stale, "\n  "))
	}
}

// The gate's teeth, on a synthetic tree — so this proves the pattern matches the
// three shapes rather than proving today's tree happens to be clean.
func TestTheAdoptionGateCatchesEachRawReaderShape(t *testing.T) {
	root := testsupport.TempDir(t)
	write := func(name, body string) {
		t.Helper()
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("scanner.go", "package p\n\nfunc f(r io.Reader) { sc := bufio.NewScanner(r); _ = sc }\n")
	write("readbytes.go", "package p\n\nfunc g(br *bufio.Reader) { _, _ = br.ReadBytes('\\n') }\n")
	write("readstring.go", "package p\n\nfunc h(br *bufio.Reader) { _, _ = br.ReadString('\\n') }\n")
	// Must NOT match: the reject-policy shape, and a delimiter that is not '\n'.
	write("clean.go", "package p\n\nfunc i(br *bufio.Reader) { _, _, _ = lineframe.ReadFrame(br, 10) }\n")
	write("other_delim.go", "package p\n\nfunc j(br *bufio.Reader) { _, _ = br.ReadString(0) }\n")
	// Must be skipped by kind, not by name.
	write("scanner_test.go", "package p\n\nfunc k(r io.Reader) { _ = bufio.NewScanner(r) }\n")
	write("sub/testdata/stub.go", "package main\n\nfunc main() { _ = bufio.NewScanner(os.Stdin) }\n")

	found, scanned, err := scanForRawLineReaders(root)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 5 {
		t.Fatalf("scanned %d files, want the 5 non-test ones outside testdata", scanned)
	}
	var got []string
	for rel := range found {
		got = append(got, rel)
	}
	sort.Strings(got)
	want := []string{"readbytes.go", "readstring.go", "scanner.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("gate matched %v, want %v", got, want)
	}
}
