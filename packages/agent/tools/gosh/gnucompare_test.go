//go:build terva_gosh && terva_gosh_gnu

package gosh

// The comparison against GNU (TKT-01M3V1C73R). It is a measurement, not a
// gate, and it never runs in CI: the tag terva_gosh_gnu keeps it out, and it
// skips unless GOSH_GNU_WORK is set. It needs podman and an image with GNU
// bash, coreutils, grep, sed, mawk, jq and ripgrep.
//
// It replays real agent scripts on both sides against the same read-only
// snapshot of this repository, mounted at the same absolute path, and
// compares stdout and the exit code. Scripts that write are left out, since
// the snapshot is read-only on both sides.
//
//	GOSH_GNU_WORK        scratch directory for the snapshot, corpus, runs and report
//	GOSH_GNU_TRANSCRIPTS colon-separated directories of session transcripts (JSONL)
//	GOSH_GNU_REWRITE     a regular expression for the checkout paths in the scripts;
//	                     every match becomes the snapshot path
//	GOSH_GNU_IMAGE       the podman image for the GNU side
//	GOSH_GNU_MAX         the most scripts to compare (default 6000)
//	GOSH_GNU_REUSE       if set, reuse the corpus and GNU outputs of the last run
//	                     and run only the in-process side again
//
//	go test -count=1 -tags 'terva_gosh terva_gosh_gnu' -run TestCompareWithGNU -timeout 3h ./packages/agent/tools/gosh/
//
// ⚠️ Keep -count=1. A cached run makes go hash every file the test opened,
// tens of thousands, and that took over 6 GB and many minutes after PASS.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gobash "github.com/mark3labs/go-bash"
	"mvdan.cc/sh/v3/syntax"

	"terva.sh/terva/packages/agent/tools"
)

const gnuTimeout = 10 * time.Second

type corpusScript struct {
	ID     int
	Script string
	Cmds   []string
}

func TestCompareWithGNU(t *testing.T) {
	work := os.Getenv("GOSH_GNU_WORK")
	if work == "" {
		t.Skip("GOSH_GNU_WORK is not set")
	}
	image := envOr("GOSH_GNU_IMAGE", "localhost/gosh-gnu:trixie")
	max, _ := strconv.Atoi(envOr("GOSH_GNU_MAX", "6000"))
	// 🚨 An empty expression matches between every two characters, and the
	// rewrite would then scramble every script without an error.
	if os.Getenv("GOSH_GNU_REWRITE") == "" && os.Getenv("GOSH_GNU_REUSE") == "" {
		t.Fatal("GOSH_GNU_REWRITE is not set")
	}
	rewrite := regexp.MustCompile(os.Getenv("GOSH_GNU_REWRITE"))

	fix := filepath.Join(work, "fixture", "terva")
	if _, err := os.Stat(fix); err != nil {
		mustRun(t, "", "mkdir", "-p", fix)
		mustRun(t, "", "sh", "-c", "git -C ../../../.. archive HEAD | tar -x -C "+fix)
	}

	run := filepath.Join(work, "run")
	saved := filepath.Join(run, "corpus.json")
	var corpus []corpusScript
	var stats string
	if os.Getenv("GOSH_GNU_REUSE") != "" {
		data, err := os.ReadFile(saved)
		if err != nil {
			t.Fatalf("GOSH_GNU_REUSE needs a previous run: %v", err)
		}
		var prev struct {
			Stats  string
			Corpus []corpusScript
		}
		if err := json.Unmarshal(data, &prev); err != nil {
			t.Fatal(err)
		}
		corpus, stats = prev.Corpus, prev.Stats+" (reused)"
		t.Logf("corpus: %s", stats)
	} else {
		corpus, stats = runGNU(t, run, fix, image, rewrite, max)
		data, err := json.Marshal(map[string]any{"Stats": stats, "Corpus": corpus})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(saved, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	type side struct {
		out  []byte
		code int
		tout bool
	}
	inproc := make([]side, len(corpus))
	jobs := make(chan int)
	var wg sync.WaitGroup
	// Writes are refused: the Sandbox root is a directory beside the snapshot.
	elsewhere := filepath.Join(work, "elsewhere")
	os.MkdirAll(elsewhere, 0o755)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				sb := tools.NewSandbox(elsewhere)
				sb.Lock()
				r := New(fix, sb)
				var stdout, stderr bytes.Buffer
				ctx, cancel := context.WithTimeout(context.Background(), gnuTimeout)
				code, err := r.run(ctx, corpus[i].Script, fix, nil, &stdout, &stderr)
				tout := ctx.Err() != nil
				cancel()
				if err != nil {
					code = -2
				}
				inproc[i] = side{stdout.Bytes(), code, tout}
			}
		}()
	}
	for i := range corpus {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	t.Logf("in-process side: %d scripts in %s", len(corpus), time.Since(start).Round(time.Second))

	// Compare.
	outcome := map[string]int{}
	total, mism := map[string]int{}, map[string]int{}
	soloTotal, soloMism := map[string]int{}, map[string]int{}
	var examples, onlyOurs []string
	for i, s := range corpus {
		n := fmt.Sprintf("%05d", s.ID)
		gout, _ := os.ReadFile(filepath.Join(run, "out", n+".out"))
		gcodeRaw, _ := os.ReadFile(filepath.Join(run, "out", n+".code"))
		gcode, _ := strconv.Atoi(strings.TrimSpace(string(gcodeRaw)))
		ip := inproc[i]
		gTout := gcode == 124 || gcode == 137
		var o string
		sameOut := bytes.Equal(gout, ip.out)
		switch {
		case gTout || ip.tout:
			o = "a side timed out"
			if !gTout {
				// GNU finished and the in-process shell did not: a hang
				// or a slowdown of ours, so it is worth reading.
				onlyOurs = append(onlyOurs, fmt.Sprintf("### %05d\n\n```sh\n%s\n```\n", s.ID, clip(s.Script, 600)))
			}
		case sameOut && gcode == ip.code:
			o = "identical"
		case sameOut && gcode != 0 && ip.code != 0:
			o = "same stdout, both failed with different codes"
		case sameOut:
			o = "same stdout, one side failed"
		case gcode == ip.code:
			o = "stdout differs, same exit code"
		default:
			o = "stdout and exit code differ"
		}
		outcome[o]++
		bad := o != "identical" && o != "a side timed out"
		for _, c := range s.Cmds {
			total[c]++
			if bad {
				mism[c]++
			}
		}
		if len(s.Cmds) == 1 {
			soloTotal[s.Cmds[0]]++
			if bad {
				soloMism[s.Cmds[0]]++
			}
		}
		if bad {
			gl, il := strings.Split(string(gout), "\n"), strings.Split(string(ip.out), "\n")
			k := 0
			for k < len(gl) && k < len(il) && gl[k] == il[k] {
				k++
			}
			line := func(ls []string) string {
				if k < len(ls) {
					return clip(ls[k], 300)
				}
				return "<end>"
			}
			examples = append(examples, fmt.Sprintf("### %s (%s) cmds=%v\n\n```sh\n%s\n```\n\nfirst difference at line %d (GNU %d lines, in-process %d lines), exit %d vs %d\n  GNU: %s\n  in:  %s\n",
				n, o, s.Cmds, clip(s.Script, 400), k+1, len(gl), len(il), gcode, ip.code, line(gl), line(il)))
		}
	}

	var rep strings.Builder
	fmt.Fprintf(&rep, "# In-process shell against GNU\n\n%s\n\n| outcome | scripts | share |\n|---|---:|---:|\n", stats)
	for _, k := range sortedByCount(outcome) {
		fmt.Fprintf(&rep, "| %s | %d | %.1f%% |\n", k, outcome[k], 100*float64(outcome[k])/float64(len(corpus)))
	}
	fmt.Fprintf(&rep, "\n## Mismatch per command (scripts using it)\n\n| command | scripts | mismatched | rate | alone | alone mismatched |\n|---|---:|---:|---:|---:|---:|\n")
	for _, c := range sortedByCount(total) {
		if total[c] < 10 {
			continue
		}
		fmt.Fprintf(&rep, "| %s | %d | %d | %.1f%% | %d | %d |\n", c, total[c], mism[c], 100*float64(mism[c])/float64(total[c]), soloTotal[c], soloMism[c])
	}
	if err := os.WriteFile(filepath.Join(work, "report.md"), []byte(rep.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "timeouts.md"), []byte(strings.Join(onlyOurs, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "mismatches.md"), []byte(strings.Join(examples, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", rep.String())
}

// runGNU builds the corpus and runs it on the GNU side.
func runGNU(t *testing.T, run, fix, image string, rewrite *regexp.Regexp, max int) ([]corpusScript, string) {
	allowed := allowedCommands(t, image)
	corpus, stats := buildCorpus(t, strings.Split(os.Getenv("GOSH_GNU_TRANSCRIPTS"), ":"), rewrite, fix, allowed, max)
	t.Logf("corpus: %s", stats)

	os.RemoveAll(run)
	for _, d := range []string{"scripts", "out"} {
		mustRun(t, "", "mkdir", "-p", filepath.Join(run, d))
	}
	for _, s := range corpus {
		if err := os.WriteFile(filepath.Join(run, "scripts", fmt.Sprintf("%05d.sh", s.ID)), []byte(s.Script), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	// 🔑 GNU grep -r and find walk a directory in the order the filesystem
	// returns it, and go-bash sorts. A tmpfs returns entries newest first,
	// so the snapshot is copied in reverse byte order to make GNU walk it
	// sorted too. The comparison then measures content, not walk order,
	// which no script can rely on anyway.
	driver := `set -e
cd /src
find . -mindepth 1 -print0 | LC_ALL=C sort -rz | while IFS= read -r -d '' p; do
  if [ -d "$p" ] && [ ! -L "$p" ]; then mkdir -p "$FIX/$p"; else mkdir -p "$FIX/$(dirname "$p")"; cp -P --preserve=mode,timestamps "$p" "$FIX/$p"; fi
done
set +e
cd "$FIX" || exit 1
for f in /gnurun/scripts/*.sh; do
  n=$(basename "$f" .sh)
  ( cd "$FIX" && timeout -k 2 ` + strconv.Itoa(int(gnuTimeout.Seconds())) + ` bash "$f" </dev/null >/gnurun/out/$n.out 2>/gnurun/out/$n.err )
  echo $? > /gnurun/out/$n.code
done`
	mustRun(t, "", "podman", "run", "--rm", "--network=none", "-e", "FIX="+fix, "-e", "TMPDIR=/tmp",
		"-v", fix+":/src:ro", "--tmpfs", fix+":rw,size=1g,mode=0755", "-v", run+":/gnurun", image, "bash", "-c", driver)
	t.Logf("GNU side: %d scripts in %s", len(corpus), time.Since(start).Round(time.Second))
	return corpus, stats
}

// TestRunOne runs GOSH_RUN in-process against the snapshot and prints both
// streams, for looking into one mismatch.
func TestRunOne(t *testing.T) {
	script, work := os.Getenv("GOSH_RUN"), os.Getenv("GOSH_GNU_WORK")
	if script == "" || work == "" {
		t.Skip("GOSH_RUN or GOSH_GNU_WORK is not set")
	}
	fix := filepath.Join(work, "fixture", "terva")
	sb := tools.NewSandbox(filepath.Join(work, "elsewhere"))
	sb.Lock()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), gnuTimeout)
	defer cancel()
	code, err := New(fix, sb).run(ctx, script, fix, nil, &stdout, &stderr)
	t.Logf("exit %d err %v\n--- stdout\n%s--- stderr\n%s", code, err, stdout.String(), stderr.String())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, clip(string(out), 2000))
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n…"
	}
	return s
}

func sortedByCount(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// noCompare are built-ins with no GNU counterpart, or that reach the network.
var noCompare = map[string]bool{"js": true, "python": true, "python3": true, "curl": true, "htmltomarkdown": true, "html-to-markdown": true, "sqlite3": true, "xan": true, "yq": true}

// allowedCommands is every name the in-process shell has that the GNU
// image also has.
func allowedCommands(t *testing.T, image string) map[string]bool {
	b, err := gobash.New(gobash.BashOptions{CustomCommands: customCommands()})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range b.Registry().Names() {
		if !noCompare[string(n)] {
			names = append(names, string(n))
		}
	}
	probe := `for c in "$@"; do command -v -- "$c" >/dev/null 2>&1 && echo "$c"; done`
	out, err := exec.Command("podman", append([]string{"run", "--rm", "--network=none", image, "bash", "-c", probe, "probe"}, names...)...).Output()
	if err != nil {
		t.Fatalf("probing the image: %v", err)
	}
	allowed := map[string]bool{}
	for _, n := range strings.Fields(string(out)) {
		allowed[n] = true
	}
	return allowed
}

var wrappers = map[string]bool{"timeout": true, "env": true, "xargs": true, "nice": true, "nohup": true, "time": true, "command": true, "exec": true}

// writerCmds change files. A script that uses one is left out.
var writerCmds = map[string]bool{"rm": true, "mv": true, "cp": true, "mkdir": true, "touch": true, "tee": true, "ln": true, "chmod": true, "rmdir": true, "split": true, "gzip": true, "gunzip": true, "tar": true, "unzip": true, "zip": true, "truncate": true, "mktemp": true}

func buildCorpus(t *testing.T, dirs []string, rewrite *regexp.Regexp, fix string, allowed map[string]bool, max int) ([]corpusScript, string) {
	seen := map[string]bool{}
	var all []corpusScript
	found, notAllowed, writes, dynamic := 0, 0, 0, 0
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 64<<20)
			for sc.Scan() {
				line := sc.Bytes()
				if !bytes.Contains(bytes.ToLower(line), []byte(`"bash"`)) {
					continue
				}
				var v any
				if json.Unmarshal(line, &v) != nil {
					continue
				}
				for _, script := range bashScripts(v) {
					found++
					script = rewrite.ReplaceAllString(script, fix)
					if seen[script] {
						continue
					}
					seen[script] = true
					cmds, ok, w := classify(script, allowed)
					switch {
					case cmds == nil && !ok:
						dynamic++
					case !ok:
						notAllowed++
					case w:
						writes++
					default:
						all = append(all, corpusScript{Script: script, Cmds: cmds})
					}
				}
			}
			return nil
		})
	}
	rng := rand.New(rand.NewSource(20261001))
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	if len(all) > max {
		all = all[:max]
	}
	for i := range all {
		all[i].ID = i
	}
	return all, fmt.Sprintf("%d shell calls, %d distinct; left out: %d that use a command one side lacks, %d that write, %d with a computed command name or no parse; compared: %d", found, len(seen), notAllowed, writes, dynamic, len(all))
}

func bashScripts(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if name, _ := x["name"].(string); strings.EqualFold(name, "bash") {
				for _, key := range []string{"input", "arguments", "args"} {
					switch a := x[key].(type) {
					case map[string]any:
						if s, ok := a["command"].(string); ok {
							out = append(out, s)
							return
						}
					case string:
						var m map[string]any
						if json.Unmarshal([]byte(a), &m) == nil {
							if s, ok := m["command"].(string); ok {
								out = append(out, s)
								return
							}
						}
					}
				}
			}
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(v)
	return out
}

// classify returns the command names, whether all are allowed, and whether
// the script writes. A nil slice with ok false means it could not tell.
func classify(script string, allowed map[string]bool) (cmds []string, ok bool, writes bool) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, false, false
	}
	set := map[string]bool{}
	funcs := map[string]bool{}
	dynamic := false
	syntax.Walk(f, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.FuncDecl:
			funcs[x.Name.Value] = true
		case *syntax.CallExpr:
			if len(x.Args) == 0 {
				return true
			}
			k := 0
			w := x.Args[0].Lit()
			// Look through a wrapper to the command it runs.
			for wrappers[w] && k+1 < len(x.Args) {
				k++
				for k < len(x.Args) {
					l := x.Args[k].Lit()
					if strings.HasPrefix(l, "-") || strings.Contains(l, "=") || (w == "timeout" && l != "" && l[0] >= '0' && l[0] <= '9') {
						k++
						continue
					}
					break
				}
				set[w] = true
				if k >= len(x.Args) {
					break
				}
				w = x.Args[k].Lit()
			}
			if w == "" {
				dynamic = true
				return true
			}
			set[w] = true
			if w == "sed" {
				for _, a := range x.Args[1:] {
					if l := a.Lit(); strings.HasPrefix(l, "-i") || strings.HasPrefix(l, "--in-place") {
						writes = true
					}
				}
			}
			if writerCmds[w] {
				writes = true
			}
		case *syntax.Redirect:
			switch x.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut:
				if x.Word != nil {
					if target := x.Word.Lit(); target != "/dev/null" && !strings.HasPrefix(target, "/tmp/") {
						writes = true
					}
				}
			}
		}
		return true
	})
	if dynamic {
		return nil, false, false
	}
	ok = true
	for c := range set {
		if funcs[c] {
			continue
		}
		cmds = append(cmds, c)
		if !allowed[c] {
			ok = false
		}
	}
	sort.Strings(cmds)
	return cmds, ok, writes
}
