//go:build terva_gosh

package gosh

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

type shell struct {
	t    *testing.T
	ws   string
	out  string // a directory beside the workspace, outside it
	sb   *tools.Sandbox
	tool *tools.BashTool
}

func newShell(t *testing.T) *shell {
	t.Helper()
	base := testsupport.TempDir(t)
	ws := filepath.Join(base, "ws")
	out := filepath.Join(base, "outside")
	for _, d := range []string{ws, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sb := tools.NewSandbox(ws)
	sb.Lock()
	return &shell{t: t, ws: ws, out: out, sb: sb, tool: &tools.BashTool{CWD: ws, Sandbox: sb, Runner: New(ws, sb)}}
}

// run executes one bash tool call and returns the text and the exit code.
func (s *shell) run(command string, timeout int) (string, int) {
	s.t.Helper()
	raw, _ := json.Marshal(map[string]any{"command": command, "timeout": timeout})
	res, err := s.tool.Execute(context.Background(), raw, nil)
	if err != nil {
		return "refused: " + err.Error(), -2
	}
	d, _ := res.Details.(map[string]any)
	code, _ := d["exit_code"].(int)
	return res.Content[0].(provider.TextBlock).Text, code
}

func (s *shell) write(rel, body string) {
	s.t.Helper()
	p := filepath.Join(s.ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func want(t *testing.T, text string, code, wantCode int, subs ...string) {
	t.Helper()
	if code != wantCode {
		t.Errorf("exit %d, want %d:\n%s", code, wantCode, text)
		return
	}
	for _, s := range subs {
		if !strings.Contains(text, s) {
			t.Errorf("output lacks %q:\n%s", s, text)
		}
	}
}

func TestAgentIdioms(t *testing.T) {
	s := newShell(t)
	s.write("pkg/a.go", "package pkg\n\nfunc Get(x int) int { return x }\n// TODO: tidy\n")
	s.write("pkg/b.go", "package pkg\n\nfunc Set() {}\n// FIXME: later\n")

	text, code := s.run(`grep -rn 'TODO\|FIXME' pkg | sort`, 0)
	want(t, text, code, 0, "pkg/a.go:4:// TODO: tidy", "pkg/b.go:4:// FIXME: later")

	text, code = s.run(`grep -n 'func Get(' pkg/a.go`, 0)
	want(t, text, code, 0, "3:func Get(x int)")

	text, code = s.run("cat > notes.md <<'EOF'\nalpha\nbeta\nEOF\nwc -l < notes.md", 0)
	want(t, text, code, 0, "2")

	text, code = s.run(`for f in pkg/*.go; do echo "$f $(grep -c func "$f")"; done`, 0)
	want(t, text, code, 0, "pkg/a.go 1", "pkg/b.go 1")

	text, code = s.run(`cd pkg && pwd && ls`, 0)
	want(t, text, code, 0, filepath.ToSlash(filepath.Join(s.ws, "pkg")), "a.go")

	text, code = s.run(`sed -i 's/tidy/done/' pkg/a.go && grep -c done pkg/a.go`, 0)
	want(t, text, code, 0, "1")
	if b, _ := os.ReadFile(filepath.Join(s.ws, "pkg/a.go")); !strings.Contains(string(b), "TODO: done") {
		t.Errorf("sed -i did not reach the host file:\n%s", b)
	}

	text, code = s.run(`sed -i.bak 's/later/now/' pkg/b.go && ls pkg`, 0)
	want(t, text, code, 0, "b.go.bak")

	text, code = s.run(`d=$(mktemp -d) && echo x > "$d/f" && cat "$d/f" && echo "$d"`, 0)
	want(t, text, code, 0, "x", "/tmp/tmp.")

	text, code = s.run(`cmp pkg/a.go pkg/a.go && echo same; cmp -s pkg/a.go pkg/b.go || echo differ`, 0)
	want(t, text, code, 0, "same", "differ")

	text, code = s.run(`echo '{"items":[1,2,3]}' | jq '.items | length'`, 0)
	want(t, text, code, 0, "3")
}

func TestAttachedOptionValues(t *testing.T) {
	s := newShell(t)
	s.write("t.txt", "b 2\na 10\nc 1\n")
	cases := []struct{ cmd, want string }{
		{"head -n1 t.txt", "b 2"},
		{"tail -n1 t.txt", "c 1"},
		{"tail -n+3 t.txt", "c 1"},
		{"head -c3 t.txt", "b 2"},
		{"sort -nk2 t.txt | head -n1", "c 1"},
		{"sort -t' ' -k1,1 t.txt | head -1", "a 10"},
		{"cut -d' ' -f1 t.txt | paste -sd,", "b,a,c"},
		{"printf 'x\ny\n' | xargs -I{} echo item-{}", "item-y"},
		{"printf 'x\ny\n' | xargs -n1 echo", "y"},
		{"grep -m1 . t.txt", "b 2"},
		{"grep -A1 '^a' t.txt", "c 1"},
		{"nl -ba t.txt", "3\tc 1"},
	}
	for _, tc := range cases {
		text, code := s.run(tc.cmd, 0)
		want(t, text, code, 0, tc.want)
	}
	s.write("u.txt", "b 2\nzz\n")
	for cmd, exact := range map[string]string{
		"grep -m1 . t.txt":          "b 2\n",
		"grep -m2 -n . t.txt":       "1:b 2\n2:a 10\n",
		"grep -c -m2 . t.txt":       "2\n",
		"grep -m1 -H 2 t.txt u.txt": "t.txt:b 2\nu.txt:b 2\n",
		// go-bash drops the ./ that GNU keeps; the grep wrapper restores it.
		"grep -rm1 b . | sort": "./t.txt:b 2\n./u.txt:b 2\n",
		// One name per line, so wc counts files.
		"ls | wc -l": "2\n",
	} {
		text, code := s.run(cmd, 0)
		if body := bodyOf(text); code != 0 || body != exact {
			t.Errorf("%s: exit %d, body %q, want %q", cmd, code, body, exact)
		}
	}
}

// bodyOf strips the bash tool's "$ command" line and footer.
func bodyOf(text string) string {
	_, rest, _ := strings.Cut(text, "\n\n")
	i := strings.LastIndex(rest, "\n\n[exit")
	if i < 0 {
		return ""
	}
	return rest[:i+1]
}

func TestSplitShortOpts(t *testing.T) {
	cases := []struct {
		in     []string
		values string
		want   string
	}{
		{[]string{"head", "-n1", "f"}, "nc", "head -n 1 f"},
		{[]string{"head", "-5", "f"}, "nc", "head -5 f"},
		{[]string{"sort", "-nrk2,2", "f"}, "ktoST", "sort -n -r -k 2,2 f"},
		{[]string{"xargs", "-I{}", "echo", "{}"}, "nILPdasE", "xargs -I {} echo {}"},
		{[]string{"paste", "-sd,"}, "d", "paste -s -d ,"},
		{[]string{"tail", "-n", "+2"}, "nc", "tail -n +2"},
		{[]string{"grep", "-rnA3", "x", "--", "-v"}, grepValueOpts, "grep -r -n -A 3 x -- -v"},
		{[]string{"ls", "--color=never", "-la"}, "IwT", "ls --color=never -l -a"},
	}
	for _, tc := range cases {
		if got := strings.Join(splitShortOpts(tc.in, tc.values), " "); got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The expectations are GNU coreutils 9.7 output in the comparison image.
func TestWcMatchesGNU(t *testing.T) {
	s := newShell(t)
	s.write("f", "a\nb\n")
	s.write("g", "x\n")
	for cmd, exact := range map[string]string{
		`printf 'a\nb\n' | wc -l`: "2\n",
		`printf 'a\nb\n' | wc`:    "      2       2       4\n",
		`printf 'a b\n' | wc -w`:  "2\n",
		"wc -l f":                 "2 f\n",
		"wc f":                    "2 2 4 f\n",
		"wc -l f g":               "2 f\n1 g\n3 total\n",
		"wc -l < f":               "2\n",
		`printf '' | wc -l`:       "0\n",
	} {
		text, code := s.run(cmd, 0)
		if body := bodyOf(text); code != 0 || body != exact {
			t.Errorf("%s: exit %d, body %q, want %q", cmd, code, body, exact)
		}
	}
}

func TestDevNull(t *testing.T) {
	s := newShell(t)
	s.write("f.txt", "kept\n")
	for cmd, exact := range map[string]string{
		"cat f.txt 2>/dev/null | head -1":      "kept\n",
		"cat missing.txt 2>/dev/null; echo ok": "ok\n",
		"echo junk > /dev/null; cat /dev/null": "",
		"ls missing &>/dev/null || echo gone":  "gone\n",
		"wc -c < /dev/null":                    "0\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

// TestGrepKeepsOperandOrder pins GNU grep 3.11 output. go-bash sorted its
// file operands, so b.txt came after a.txt.
func TestGrepKeepsOperandOrder(t *testing.T) {
	s := newShell(t)
	s.write("b.txt", "x1\nq\n")
	s.write("a.txt", "q\nx2\n")
	for cmd, exact := range map[string]string{
		"grep x b.txt a.txt":                                 "b.txt:x1\na.txt:x2\n",
		"grep -c x b.txt a.txt":                              "b.txt:1\na.txt:1\n",
		"grep -h x b.txt a.txt":                              "x1\nx2\n",
		"grep x b.txt -n a.txt":                              "b.txt:1:x1\na.txt:2:x2\n",
		"grep -A1 x b.txt a.txt":                             "b.txt:x1\nb.txt-q\n--\na.txt:x2\n",
		"grep -l x b.txt missing a.txt 2>/dev/null; echo $?": "b.txt\na.txt\n2\n",
		"grep -q x missing b.txt 2>/dev/null; echo $?":       "0\n",
		"grep zz b.txt a.txt; echo $?":                       "1\n",
		// Options after the operands, as agents write them.
		"grep -n x b.txt -A 1":    "1:x1\n2-q\n",
		"grep x b.txt a.txt -A1":  "b.txt:x1\nb.txt-q\n--\na.txt:x2\n",
		"grep x b.txt a.txt -m 1": "b.txt:x1\na.txt:x2\n",
		"grep -n x -- b.txt":      "1:x1\n",
		"grep x b.txt -c":         "1\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

// TestRecursiveGrepAndLsAll pins GNU grep 3.11, coreutils 9.7 and findutils
// output in the C locale.
func TestRecursiveGrepAndLsAll(t *testing.T) {
	s := newShell(t)
	s.write("f.txt", "x1\n")
	s.write("sub/g.txt", "x2\n")
	s.write(".hid", "")
	for cmd, exact := range map[string]string{
		"grep -r x f.txt":                        "x1\n",
		"grep -rH x f.txt":                       "f.txt:x1\n",
		"grep -rc x f.txt":                       "1\n",
		"grep -rn x sub":                         "sub/g.txt:1:x2\n",
		"grep -r x | sort":                       "f.txt:x1\nsub/g.txt:x2\n",
		"ls -a":                                  ".\n..\n.hid\nf.txt\nsub\n",
		"ls -1a sub":                             ".\n..\ng.txt\n",
		"ls -A":                                  ".hid\nf.txt\nsub\n",
		"ls -a | wc -l":                          "5\n",
		"ls missing 2>/dev/null; echo $?":        "2\n",
		"echo f.txt | xargs grep -q zz; echo $?": "123\n",
		"echo f.txt | xargs grep -q x1; echo $?": "0\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

// TestGrepExcludeDirAndLsOperands pins GNU grep 3.11 and coreutils 9.7
// output in the C locale.
func TestGrepExcludeDirAndLsOperands(t *testing.T) {
	s := newShell(t)
	for _, f := range []string{"zd/f", "ad/f", ".git/x/f", "node/y/f"} {
		s.write(f, "hit\n")
	}
	s.write("b.sh", "")
	s.write("a.ps1", "")
	s.write("p", "--x)\n")
	for cmd, exact := range map[string]string{
		"ls zd ad b.sh a.ps1": "a.ps1\nb.sh\n\nad:\nf\n\nzd:\nf\n",
		"ls -r b.sh a.ps1":    "b.sh\na.ps1\n",
		"grep -r --exclude-dir=.git --exclude-dir=no* hit . | sort": "./ad/f:hit\n./zd/f:hit\n",
		"grep -r hit . --exclude-dir=zd | sort":                     "./.git/x/f:hit\n./ad/f:hit\n./node/y/f:hit\n",
		`grep -c -- "--x)" p`:                                       "1\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

// TestGrepModesAnywhere pins GNU grep 3.11: an option counts wherever it
// stands, two matchers conflict, and -f patterns get the basic-mode
// translation.
func TestGrepModesAnywhere(t *testing.T) {
	s := newShell(t)
	s.write("f", "a.b\naxb\nfoo\nbar\n")
	s.write("pats", "a\\|fo\n")
	for cmd, exact := range map[string]string{
		`grep -F -G "a.b" f 2>&1; echo $?`: "grep: conflicting matchers specified\n2\n",
		`grep "foo|bar" f -E`:              "foo\nbar\n",
		`grep "foo|bar" f -F; echo $?`:     "1\n",
		"grep f -e foo; echo $?":           "foo\n0\n",
		"grep -f pats f":                   "a.b\naxb\nfoo\nbar\n",
		"grep -c -m1 a f":                  "1\n",
		"seq 1 200000 | grep -m2 7":        "7\n17\n",
		// After --, an argument that looks like an option is a file.
		"grep -c -- foo -- f 2>/dev/null; echo $?":  "f:1\n2\n",
		"grep -c -- foo --x f 2>/dev/null; echo $?": "f:1\n2\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

// TestNetworkIsOff checks that curl and wget refuse with a reason, whatever
// go-bash's own default is.
func TestNetworkIsOff(t *testing.T) {
	s := newShell(t)
	for _, cmd := range []string{"curl -s https://example.com", "wget -qO- https://example.com"} {
		text, code := s.run(cmd, 0)
		want(t, text, code, 127, "the network is off")
	}
}

// TestNilSandboxFailsClosed checks that a runner built without a Sandbox
// holds a locked one at its root. The mount alone already hides every path
// outside the workspace, so a write test could not tell a missing Sandbox
// from a present one. This asks the Sandbox directly.
func TestNilSandboxFailsClosed(t *testing.T) {
	s := newShell(t)
	r := New(s.ws, nil)
	if r.sb == nil {
		t.Fatal("a runner built without a Sandbox has none")
	}
	if err := r.sb.CheckPath(filepath.Join(s.out, "w.txt")); err == nil {
		t.Error("the fallback Sandbox lets a write outside its root through, so it is not locked")
	}
	if err := r.sb.CheckPath(filepath.Join(s.ws, "ok.txt")); err != nil {
		t.Errorf("the fallback Sandbox refuses a write inside its root: %v", err)
	}
}

// TestReviewRound2 pins GNU grep 3.11 and findutils xargs, and js exit statuses,
// for the cases review round 2 on #1554 found.
func TestReviewRound2(t *testing.T) {
	s := newShell(t)
	s.write("f", "a\nx\nb\nc\nx\nd\n")
	s.write("g", "x\n")
	s.write("empty", "")
	s.write("p1", "x\n")
	for cmd, exact := range map[string]string{
		"echo f | xargs grep -nA1 b":                               "3:b\n4-c\n",
		"grep -q -m1 x f; echo $?":                                 "0\n",
		"grep -m1 -o x f 2>&1; echo $?":                            "grep: -m with -o is not supported in this shell\n2\n",
		"grep -m0 x f; echo $?":                                    "1\n",
		"grep -f empty -f p1 f":                                    "x\nx\n",
		"grep -v -f empty f; echo $?":                              "a\nx\nb\nc\nx\nd\n0\n",
		"grep --context 0 x g":                                     "x\n",
		"grep -c -e --max-count g":                                 "0\n",
		"grep -c --max-count 1 x f":                                "1\n",
		"grep -c -f empty f; echo $?":                              "1\n",
		"grep -F -v -f empty f | wc -l":                            "6\n",
		"grep -f empty f; echo $?":                                 "1\n",
		"grep --context=1 b f":                                     "x\nb\nc\n",
		"grep -1 b f":                                              "x\nb\nc\n",
		"grep --after-context=1 b f g":                             "f:b\nf-c\n",
		"js -e 'process.exit(3)'; echo $?":                         "3\n",
		`js -e 'throw new Error("__exit:0")' 2>/dev/null; echo $?`: "1\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

// TestLongOptionValuesAndSedPermutes pins GNU grep 3.11 and GNU sed 4.9:
// a long option takes its value from the next argument, and sed reads -i
// after the script.
func TestLongOptionValuesAndSedPermutes(t *testing.T) {
	s := newShell(t)
	s.write("src/a.go", "TODO\n")
	s.write("src/a.md", "TODO\n")
	s.write("node_modules/x.go", "TODO\n")
	s.write("s.txt", "old\n")
	s.write("h", "a --include b\n")
	for cmd, exact := range map[string]string{
		// A pattern that spells a long option stays a pattern.
		"grep -rn -e --include . | sort":                    "./h:1:a --include b\n",
		"grep -rn --include '*.go' TODO . | sort":           "./node_modules/x.go:1:TODO\n./src/a.go:1:TODO\n",
		"grep --exclude-dir node_modules -rn TODO . | sort": "./src/a.go:1:TODO\n./src/a.md:1:TODO\n",
		"grep -r --max-count 1 TODO src | sort":             "src/a.go:TODO\nsrc/a.md:TODO\n",
		"sed 's/old/new/' -i s.txt; cat s.txt":              "new\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

// TestRound7 pins GNU grep 3.11 and coreutils 9.7 in the C locale: a
// pattern spelled like a flag is still a pattern, and ls -a gives each
// directory its own . and ..
func TestRound7(t *testing.T) {
	s := newShell(t)
	s.write("a.sh", "run -q now\n")
	s.write("b.sh", "run -q later\n")
	s.write("c.sh", "x -h y\n")
	s.write("d1/x", "")
	s.write("d2/.y", "")
	s.write("f", "")
	for cmd, exact := range map[string]string{
		"grep -n -e -q a.sh b.sh":                "a.sh:1:run -q now\nb.sh:1:run -q later\n",
		"grep -e -h a.sh c.sh":                   "c.sh:x -h y\n",
		"grep -c -m1 -e -o a.sh b.sh":            "a.sh:0\nb.sh:0\n",
		"grep -l -A1 run a.sh b.sh":              "a.sh\nb.sh\n",
		"grep -c -C1 run a.sh b.sh":              "a.sh:1\nb.sh:1\n",
		"ls -a d2 d1":                            "d1:\n.\n..\nx\n\nd2:\n.\n..\n.y\n",
		"ls -a d1 f d2":                          "f\n\nd1:\n.\n..\nx\n\nd2:\n.\n..\n.y\n",
		"ls -1a d1 missing 2>/dev/null; echo $?": "d1:\n.\n..\nx\n2\n",
	} {
		text, _ := s.run(cmd, 0)
		if body := bodyOf(text); body != exact {
			t.Errorf("%s: body %q, want %q", cmd, body, exact)
		}
	}
}

func TestTmpPersistsAcrossCalls(t *testing.T) {
	s := newShell(t)
	// A name of this run's own, absent from the host before the run, so a
	// leftover file cannot pass for an escape.
	name := "/tmp/gosh-persist-" + filepath.Base(s.ws) + "-" + strconv.Itoa(os.Getpid()) + ".txt"
	if _, err := os.Stat(name); err == nil {
		t.Fatalf("%s already exists on the host", name)
	}
	text, code := s.run("echo kept > "+name, 0)
	want(t, text, code, 0)
	text, code = s.run("cat "+name, 0)
	want(t, text, code, 0, "kept")
	if _, err := os.Stat(name); err == nil {
		os.Remove(name)
		t.Error("/tmp in the shell reached the host /tmp")
	}
}

func TestJail(t *testing.T) {
	s := newShell(t)
	if err := os.WriteFile(filepath.Join(s.out, "secret.txt"), []byte("outside-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.ToSlash(filepath.Join(s.out, "secret.txt"))

	// A path outside the workspace is not visible at all.
	for _, cmd := range []string{
		"cat " + outside,
		"cat ../outside/secret.txt",
		"head -n1 " + outside,
		"grep outside " + outside,
		"cp " + outside + " stolen.txt",
	} {
		text, _ := s.run(cmd, 0)
		t.Logf("%s", text)
		if strings.Contains(text, "outside-bytes") {
			t.Errorf("%s read a file outside the workspace:\n%s", cmd, text)
		}
	}
	if _, err := os.Stat(filepath.Join(s.ws, "stolen.txt")); err == nil {
		if b, _ := os.ReadFile(filepath.Join(s.ws, "stolen.txt")); strings.Contains(string(b), "outside-bytes") {
			t.Error("cp copied a file from outside the workspace")
		}
	}

	// A symlink inside the workspace that points out of it.
	if err := os.Symlink(filepath.Join(s.out, "secret.txt"), filepath.Join(s.ws, "link.txt")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	text, _ := s.run("cat link.txt", 0)
	t.Logf("%s", text)
	if strings.Contains(text, "outside-bytes") {
		t.Errorf("a symlink led out of the workspace:\n%s", text)
	}

	// A directory symlink in the middle of a path. O_NOFOLLOW alone covers
	// only the last component, so this is the case that proves rwfs walks
	// every component. One link comes from the host, as a checkout would
	// leave it, and the script tries to make another.
	if err := os.Symlink(s.out, filepath.Join(s.ws, "dirlink")); err != nil {
		t.Fatal(err)
	}
	// Positive control: on the host the link does reach the secret.
	if b, err := os.ReadFile(filepath.Join(s.ws, "dirlink", "secret.txt")); err != nil || string(b) != "outside-bytes" {
		t.Fatalf("the host cannot follow dirlink, so this case proves nothing: %q %v", b, err)
	}
	for _, cmd := range []string{
		"cat dirlink/secret.txt",
		"ln -s " + filepath.ToSlash(s.out) + " made; cat made/secret.txt",
		"ln -s / root; cat root" + filepath.ToSlash(outside),
		"echo pwned > dirlink/via-link.txt",
		"ln -s " + filepath.ToSlash(s.out) + " made2; echo pwned > made2/via-made.txt",
	} {
		text, _ := s.run(cmd, 0)
		t.Logf("%s", text)
		if strings.Contains(text, "outside-bytes") {
			t.Errorf("%s read through a directory symlink:\n%s", cmd, text)
		}
	}
	for _, name := range []string{"via-link.txt", "via-made.txt"} {
		if _, err := os.Stat(filepath.Join(s.out, name)); err == nil {
			t.Errorf("a write went through a directory symlink: %s", name)
		}
	}
	for _, name := range []string{"made", "made2", "root"} {
		if _, err := os.Lstat(filepath.Join(s.ws, name)); err == nil {
			t.Errorf("the script created the symlink %s", name)
		}
	}

	// A write outside the workspace never reaches the host.
	hostTarget := filepath.Join(s.out, "written.txt")
	s.run("echo pwned > "+filepath.ToSlash(hostTarget), 0)
	s.run("echo pwned > ../outside/written2.txt", 0)
	for _, p := range []string{hostTarget, filepath.Join(s.out, "written2.txt")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("a write reached the host outside the workspace: %s", p)
		}
	}
}

// TestSecretRootParentStaysPut checks that a script cannot move or delete
// a directory that holds a secret root. The deny list matches by path, so a
// renamed parent would carry the secret out from under its root.
func TestSecretRootParentStaysPut(t *testing.T) {
	s := newShell(t)
	s.write("cfg/terva/auth.json", `{"token":"do-not-read"}`)
	s.sb.AddSecretRoot(filepath.Join(s.ws, "cfg", "terva"))
	for _, cmd := range []string{
		"mv cfg c2; cat c2/terva/auth.*",
		"mv cfg/terva cfg/t2; cat cfg/t2/auth.*",
		"rm -rf cfg",
		"rm -r cfg/terva",
	} {
		text, _ := s.run(cmd, 0)
		t.Logf("%s", text)
		if strings.Contains(text, "do-not-read") {
			t.Errorf("%s read a secret through a moved parent:\n%s", cmd, text)
		}
		b, err := os.ReadFile(filepath.Join(s.ws, "cfg", "terva", "auth.json"))
		if err != nil || !strings.Contains(string(b), "do-not-read") {
			t.Fatalf("%s moved or deleted the secret root: %v", cmd, err)
		}
	}
}

func TestSecretDenyList(t *testing.T) {
	s := newShell(t)
	s.write(".creds/auth.json", `{"token":"do-not-read"}`)
	s.sb.AddSecretRoot(filepath.Join(s.ws, ".creds"))
	for _, cmd := range []string{
		"cat .creds/auth.json",
		"grep token .creds/auth.json",
		"head .creds/auth.json",
		"cp .creds/auth.json copy.json && cat copy.json",
		"tar -cf - .creds | tar -tvf -",
		`js -e 'console.log(require("fs").readFileSync(".creds/auth.json"))'`,
		"sed -n p .creds/auth.json",
		// These two get past the bash tool's static argument scan, so they
		// reach the shell and test the filesystem guard itself.
		`f=.cr; cat "${f}eds/auth.json"`,
		"cat .cr*/auth.*",
	} {
		text, _ := s.run(cmd, 0)
		t.Logf("%s", text)
		if strings.Contains(text, "do-not-read") {
			t.Errorf("%s read a denied file:\n%s", cmd, text)
		}
	}
	reached := 0
	for _, cmd := range []string{`f=.cr; cat "${f}eds/auth.json"`, "cat .cr*/auth.*"} {
		if text, _ := s.run(cmd, 0); strings.HasPrefix(text, "$ ") && strings.Contains(text, "jailed") {
			reached++
		}
	}
	if reached != 2 {
		t.Errorf("only %d of 2 indirect reads reached the filesystem guard and were refused there", reached)
	}
	text, _ := s.run(`f=.cr; echo overwritten > "${f}eds/auth.json"`, 0)
	if b, _ := os.ReadFile(filepath.Join(s.ws, ".creds/auth.json")); !strings.Contains(string(b), "do-not-read") {
		t.Errorf("a write replaced a denied file:\n%s", text)
	}
}

func TestPythonPointsAtJS(t *testing.T) {
	s := newShell(t)
	for _, py := range []string{"python3", "python"} {
		text, code := s.run(py+` -c 'print(1)'`, 0)
		want(t, text, code, 127, "python is not available", "Use js instead", "jq")
	}
}

func TestJS(t *testing.T) {
	s := newShell(t)
	text, code := s.run(`echo '{"items":[1,2,3]}' | js -e 'const d = JSON.parse(stdin); console.log(d.items.length)'`, 0)
	want(t, text, code, 0, "3")

	s.write("data.json", `{"name":"terva"}`)
	text, code = s.run(`js -e 'const fs = require("fs"); const d = JSON.parse(fs.readFileSync("data.json", "utf8")); fs.writeFileSync("out.txt", d.name.toUpperCase()); console.log({ok: true})' && cat out.txt`, 0)
	want(t, text, code, 0, `"ok": true`, "TERVA")

	text, code = s.run(`js -e 'process.exit(3)'`, 0)
	want(t, text, code, 3)

	text, code = s.run(`js -e 'throw new Error("boom")'`, 0)
	want(t, text, code, 1, "boom")

	text, code = s.run(`js -e 'require("child_process")'`, 0)
	want(t, text, code, 1, "is not available in js")

	text, code = s.run(`js -e 'console.log(args.join("+"))' a b c`, 0)
	want(t, text, code, 0, "a+b+c")
}

func TestMissingCommandHint(t *testing.T) {
	s := newShell(t)
	text, code := s.run("git status", 0)
	want(t, text, code, 127, "git: command not found", "[hint] git: not available", "The built-in commands are:", " jq ")
}

func TestTimeoutStopsTheScript(t *testing.T) {
	s := newShell(t)
	for _, cmd := range []string{
		"while true; do :; done",
		"while true; do echo spin; done",
		"sleep 30",
		`js -e 'while (true) {}'`,
		"seq 1 1000000000 | wc -l",
		"seq 1 1000000000 | sort | head -1",
		"x=a; while true; do x=$x$x; done",
		`awk 'BEGIN { while (1) {} }'`,
		`jq -n 'def f: f; f'`,
		"while true; do echo y; done | head -n 1000000000 | wc -l",
	} {
		start := time.Now()
		text, _ := s.run(cmd, 1)
		took := time.Since(start)
		t.Logf("%-40s stopped after %s", cmd, took.Round(10*time.Millisecond))
		// The grace before the runner abandons a script is 2 s.
		if took > 4*time.Second {
			t.Errorf("%q ran %s past a 1 s timeout", cmd, took)
		}
		if !strings.Contains(text, "timed out") {
			t.Errorf("%q did not report a timeout:\n%.300s", cmd, text)
		}
	}
}
