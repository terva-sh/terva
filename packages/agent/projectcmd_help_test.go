package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A help probe must never persist anything.
//
// `terva project`'s subverbs read their argument straight out of rest[0], and
// only the TOP level honored -h/--help/help. So `terva project model --help`
// wrote "--help" into .terva/config.json as the project's model, and
// `terva project ext disable --help` added it to the project's disable list.
// Every other verb in this binary honors the help forms at every level.
//
// The assertion is on the FILE, not on the exit code: the defect was a
// persisted state change, and a run that prints help and still writes would
// pass any check that only looked at what came back.
func TestAHelpProbeNeverWritesProjectConfig(t *testing.T) {
	for _, argv := range [][]string{
		{"project", "model", "--help"},
		{"project", "model", "-h"},
		{"project", "model", "help"},
		{"project", "provider", "--help"},
		{"project", "ext", "adopt", "--help"},
		{"project", "ext", "drop", "--help"},
		{"project", "ext", "disable", "--help"},
		{"project", "ext", "enable", "--help"},
		{"project", "ext", "--help"},
		{"project", "ext", "help"},
		{"project", "init", "--help"},
	} {
		t.Run(argv[len(argv)-2]+" "+argv[len(argv)-1], func(t *testing.T) {
			dir := testsupport.TempDir(t)
			chdirTemp(t, dir)

			handled, err := runProjectCommand(argv)
			if !handled {
				t.Fatalf("%v was not handled", argv)
			}
			if err != nil {
				t.Errorf("%v returned an error rather than printing help: %v", argv, err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".terva")); !os.IsNotExist(err) {
				t.Errorf("%v created ./.terva — a help probe persisted state", argv)
			}
		})
	}
}

// The complement: the guard must not swallow the real verbs. Without this,
// "always print help and return" would pass the test above.
func TestTheHelpGuardStillLetsTheRealVerbsWrite(t *testing.T) {
	dir := testsupport.TempDir(t)
	chdirTemp(t, dir)

	if handled, err := runProjectCommand([]string{"project", "model", "gpt-5.6"}); !handled || err != nil {
		t.Fatalf("setting the model: handled=%v err=%v", handled, err)
	}
	m, _, err := readProjectConfigMap()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(m["model"]); got != `"gpt-5.6"` {
		t.Errorf("project model = %s, want \"gpt-5.6\" — the help guard ate a real argument", got)
	}
}

// A value that merely CONTAINS a help form is not a probe. The bare word is a
// legitimate persona name, and only the leading verb position may claim it.
func TestABareHelpWordIsStillAValueAwayFromTheVerbPosition(t *testing.T) {
	dir := testsupport.TempDir(t)
	chdirTemp(t, dir)

	if handled, err := runProjectCommand([]string{"project", "init", "--persona", "help"}); !handled || err != nil {
		t.Fatalf("init with a persona named help: handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".terva", "personas", "help.md")); err != nil {
		t.Errorf("a persona named \"help\" was read as a help probe: %v", err)
	}
}

// The census: every subverb the router dispatches must appear in the probe
// table above. A verb added later that takes an argument is exactly the shape
// that broke here, and a table nobody updates cannot fail when one is ADDED.
func TestEveryProjectSubverbIsProbedForHelp(t *testing.T) {
	// The subverbs the table exercises, by the name the router switches on.
	probed := map[string]bool{
		"model": true, "provider": true, "ext": true, "extensions": true, "init": true,
	}
	// Verbs that take no argument at all, so no probe can persist one. Each is
	// a deliberate claim: check the signature before adding a name here.
	noArgs := map[string]string{
		"status":  "runProjectStatus() takes nothing",
		"info":    "alias of status",
		"trust":   "runProjectTrust(false) takes nothing",
		"untrust": "runProjectTrust(true) takes nothing",
		"help":    "the help verb itself",
		"-h":      "the help verb itself",
		"--help":  "the help verb itself",
		"":        "the empty verb prints help",
	}

	cases := routerCaseLabels(t, "runProjectCommand")
	if len(cases) < 8 {
		t.Fatalf("found only %d case labels in runProjectCommand — the scan is not reading the switch", len(cases))
	}
	for _, name := range cases {
		if probed[name] || noArgs[name] != "" {
			continue
		}
		t.Errorf("`terva project %s` is dispatched but never probed with --help; add it to "+
			"TestAHelpProbeNeverWritesProjectConfig, or to noArgs with the reason it cannot persist one", name)
	}
}

// routerCaseLabels returns every string case label in the top-level switch of
// the named function.
func routerCaseLabels(t *testing.T, fn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "projectcmd.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			return true
		}
		ast.Inspect(fd.Body, func(m ast.Node) bool {
			cc, ok := m.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						out = append(out, s)
					}
				}
			}
			return true
		})
		return false
	})
	return out
}
