package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/testsupport"
)

// The merge rule of a chain that no person began (decision 0029 rules 14 and
// 15, TKT-01M7N39E0).
//
// 🔑 These tests try a merge and then read trunk. The team's directory is a
// clone of a bare repository, which stands for the forge. Programs named
// just, tea, gh, glab and curl stand first on PATH, and each one moves trunk
// on the forge when its arguments say merge. A test that read only the text
// of a refusal would pass against a rule that refuses one route and misses
// three.

// mergeRoutes are the ways to trunk that this repository documents, and the
// ones a model finds next. The pushes come first: a program that merges
// leaves trunk ahead of the branch, and a push after it would be refused by
// git and prove nothing.
var mergeRoutes = []string{
	"git commit -q --allow-empty -m work && git push origin HEAD:trunk",
	"git commit -q --allow-empty -m work && git push origin feat/x:refs/heads/trunk",
	"git commit -q --allow-empty -m work && git -c push.default=upstream push",
	"git commit -q --allow-empty -m work && git config push.default upstream && git push",
	"just pr-merge 12",
	"./scripts/pr.sh merge 12",
	"tea pulls merge --style rebase 12",
	"curl -s -X POST https://forge.example/api/v1/repos/o/r/pulls/12/merge",
	"gh pr merge 12 --squash",
	"gh pr --repo owner/repo merge 12",
	"glab mr merge 12",
}

// forge is the bare repository that stands for the forge, and the file that
// the programs write each call with no merge to.
type forge struct {
	bare, log, clone string
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newForge makes cwd a clone on the branch feat/x, which follows trunk, as
// just scratch leaves a worktree of this repository.
func newForge(t *testing.T, cwd string) forge {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the programs that stand for a forge's CLI are shell scripts")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// No configuration of the machine reaches these repositories: a hook, a
	// signing key or a push.default of the person's would change the test.
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
		"GIT_AUTHOR_NAME": "helm", "GIT_AUTHOR_EMAIL": "helm@example.invalid",
		"GIT_COMMITTER_NAME": "helm", "GIT_COMMITTER_EMAIL": "helm@example.invalid",
	} {
		t.Setenv(k, v)
	}
	dir := testsupport.TempDir(t)
	f := forge{bare: filepath.Join(dir, "forge.git"), log: filepath.Join(dir, "calls.log"), clone: cwd}
	gitOut(t, dir, "init", "-q", "--bare", "-b", "trunk", f.bare)
	gitOut(t, cwd, "init", "-q", "-b", "trunk")
	gitOut(t, cwd, "remote", "add", "origin", f.bare)
	gitOut(t, cwd, "commit", "-q", "--allow-empty", "-m", "the first commit")
	gitOut(t, cwd, "push", "-q", "origin", "trunk")
	gitOut(t, cwd, "remote", "set-head", "origin", "trunk")
	gitOut(t, cwd, "switch", "-q", "-c", "feat/x", "--track", "origin/trunk")

	// One program for every name. It merges when its arguments say so, and
	// it writes any other call to the log. land is a recipe of the person's
	// own, which the rule knows only from the configuration.
	script := fmt.Sprintf(`#!/bin/sh
set -e
case " $* " in
*merge*|*" land "*)
	export GIT_DIR=%q
	git update-ref refs/heads/trunk "$(git commit-tree -m merged -p trunk 'trunk^{tree}')"
	echo "merged by $(basename "$0")"
	;;
*)
	echo "$(basename "$0") $*" >> %q
	;;
esac
`, f.bare, f.log)
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(filepath.Join(cwd, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(cwd, "scripts", "pr.sh")}
	for _, name := range []string{"just", "tea", "gh", "glab", "curl"} {
		paths = append(paths, filepath.Join(bin, name))
	}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// ⚠️ macOS ships no timeout, and a test that runs "timeout 100 just ..."
	// must not need the one of the machine. This one drops the time and runs
	// the command.
	if err := os.WriteFile(filepath.Join(bin, "timeout"), []byte("#!/bin/sh\nshift\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f forge) trunk(t *testing.T) string {
	t.Helper()
	return gitOut(t, f.bare, "rev-parse", "refs/heads/trunk")
}

// mergeCrew is the crew talkoot in a clone of a forge, after one turn of
// helm's. posture is helm's. A coordinator has plan by default, and plan has
// no bash, so a test that runs a command names another one.
func mergeCrew(t *testing.T, p *runScript, posture string) (*Workspace, talkootSeat, *stepClock, forge) {
	t.Helper()
	cwd := talkootHome(t)
	f := newForge(t, cwd)
	routinesConfig(t, `{"max": 5}`)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, p.serve)
	text := string(crewText(cwd))
	if posture != "" {
		text = strings.Replace(text, "role: coordinator\n", "role: coordinator\n    posture: "+posture+"\n", 1)
	}
	if _, err := w.talkootCreate(t.Context(), "crew", []byte(text)); err != nil {
		t.Fatal(err)
	}
	return w, helmAfterOneTurn(t, w), clock, f
}

// tell posts a person's message to helm, and waits for the turn that it
// starts to end.
func tell(t *testing.T, w *Workspace, body string) {
	t.Helper()
	turns := memberView(t, w, "crew", "helm").Status.Turns
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"helm"}, body, nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn after "+body, func() bool {
		m := memberView(t, w, "crew", "helm")
		return m.Status.Turns > turns && !m.Status.Working
	})
}

const mergeDenied = "denied by the unattended merge rule"

// 🚨 Criteria 1, 3 and 5. A run tries each route to trunk, and trunk does not
// move. It may still push its own branch and open a pull request.
func TestAnUnattendedRunCannotMergeByAnyRoute(t *testing.T) {
	seq := append([]string{}, mergeRoutes...)
	seq = append(seq,
		"git commit -q --allow-empty -m work && git push -u origin feat/x",
		"tea pulls create --title 'a fix' --head feat/x",
	)
	p := &runScript{reply: "Pull request 12 waits for a person to merge it.", seq: seq}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	before := f.trunk(t)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)

	if after := f.trunk(t); after != before {
		t.Fatalf("trunk moved from %s to %s in a run that no person began", before, after)
	}
	bodies := p.runs()
	last := bodies[len(bodies)-1]
	if n := strings.Count(last, mergeDenied); n != len(mergeRoutes) {
		t.Fatalf("the run read %d refusals, want one for each of the %d routes", n, len(mergeRoutes))
	}
	// Each refusal says what the rule found, so the model does not try the
	// same route in another spelling.
	for _, want := range []string{
		"its push moves the protected branch trunk",
		"terva cannot read where its push goes",
		"the recipe pr-merge merges a pull request",
		"it merges a pull request (pr merge)",
		"it merges a pull request (tea pulls merge)",
		"it names a forge's merge endpoint",
		"it merges a pull request (gh pr merge)",
		"it merges a pull request (gh pr owner/repo merge)",
		"it merges a pull request (glab mr merge)",
		"Leave the merge to a person",
	} {
		if !strings.Contains(last, want) {
			t.Errorf("the run did not read %q", want)
		}
	}
	// 🚨 Criterion 3. The run's own branch is on the forge, and the command
	// that opens a pull request ran.
	if got, want := gitOut(t, f.bare, "rev-parse", "refs/heads/feat/x"), gitOut(t, f.clone, "rev-parse", "HEAD"); got != want {
		t.Fatalf("the forge holds %s as the run's branch, want its head %s", got, want)
	}
	log, _ := os.ReadFile(f.log)
	if !strings.Contains(string(log), "tea pulls create --title a fix --head feat/x") {
		t.Fatalf("the command that opens a pull request did not run: %q", log)
	}
}

// 🚨 The run's member is in the same chain when it reads the report, and no
// person began that chain. Its merge is refused too.
func TestAMemberInARoutinesChainCannotMerge(t *testing.T) {
	p := &runScript{reply: "Pull request 12 is green."}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	p.set(func() { p.memberBash, p.memberBashOn = `{"command":"just pr-merge 12"}`, reportMark })
	before := f.trunk(t)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "helm to read the refusal", func() bool {
		rest := p.rest()
		return len(rest) > 0 && strings.Contains(rest[len(rest)-1], mergeDenied)
	})
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
	if after := f.trunk(t); after != before {
		t.Fatalf("trunk moved from %s to %s in a chain that no person began", before, after)
	}
}

// 🚨 Criterion 2, and the proof that the tests above can see a merge. In a
// chain that a person began the rule has no word, each route runs, and each
// one moves trunk.
func TestAChainThatAPersonBeganMergesByEachRoute(t *testing.T) {
	p := &runScript{reply: "unused"}
	w, _, _, f := mergeCrew(t, p, "workspace")
	for i, route := range mergeRoutes {
		before := f.trunk(t)
		on := fmt.Sprintf("Merge it by route %d.", i)
		p.set(func() {
			p.memberBash, p.memberBashOn = string(mustJSON(t, map[string]string{"command": route})), on
		})
		tell(t, w, on)
		if after := f.trunk(t); after == before {
			rest := p.rest()
			t.Fatalf("route %d did not move trunk in a chain that a person began: %s\nthe last request: %s", i, route, rest[len(rest)-1])
		}
		if rest := p.rest(); strings.Contains(rest[len(rest)-1], mergeDenied) {
			t.Fatalf("route %d was refused in a chain that a person began: %s", i, route)
		}
	}
}

// 🚨 Criterion 2, in the posture that asks. The merge of a member in a chain
// that a person began opens the card that every other command opens, and a
// person's approval lets it run.
func TestAMergeInAChainThatAPersonBeganAsksThePerson(t *testing.T) {
	p := &runScript{reply: "unused"}
	w, _, _, f := mergeCrew(t, p, "ask")
	before := f.trunk(t)
	p.set(func() { p.memberBash, p.memberBashOn = `{"command":"just pr-merge 12"}`, "Merge pull request 12." })
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"helm"}, "Merge pull request 12.", nil, ""); err != nil {
		t.Fatal(err)
	}
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the permission card", func() bool {
		cards := inbox(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	if card.Kind != ctrlproto.TalkootCardPermission {
		t.Fatalf("the card is a %s, want a permission", card.Kind)
	}
	if after := f.trunk(t); after != before {
		t.Fatal("the merge ran before a person approved it")
	}
	if err := w.Approve(t.Context(), card.Session, card.ID, permission.ConfirmDecision{Allow: true}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the merge that the person approved", func() bool { return f.trunk(t) != before })
}

// 🚨 The rule reads the repository of the session's directory, and each bash
// call starts in that directory. A cd in one call does not reach the next
// one. So a run cannot cd to another clone in one call and push there in the
// next: the push runs in the repository that the rule read.
//
// The other clone is inside the session's directory, as a vendored
// repository is. The jail of the sandbox refuses a cd out of that directory.
func TestACdInOneCallDoesNotMoveThePushOfTheNext(t *testing.T) {
	p := &runScript{reply: "Done.", seq: []string{"cd other", "git push"}}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	// A second clone on trunk, with a commit that a push from it gives to
	// the forge.
	other := filepath.Join(f.clone, "other")
	gitOut(t, f.clone, "clone", "-q", f.bare, other)
	gitOut(t, other, "commit", "-q", "--allow-empty", "-m", "work on trunk")
	before := f.trunk(t)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	if after := f.trunk(t); after != before {
		t.Fatalf("trunk moved from %s to %s: the push ran in the directory of an earlier call", before, after)
	}
	bodies := p.runs()
	if last := bodies[len(bodies)-1]; strings.Contains(last, mergeDenied) {
		t.Fatalf("the rule refused a call, so this test did not see the push run: %s", last)
	}
	// 🚨 The proof that this test can see such a push. In a chain that a
	// person began, the cd and the push in one call move trunk.
	on := "Push the other clone."
	p.set(func() {
		p.memberBash, p.memberBashOn = `{"command":"cd other && git push"}`, on
	})
	tell(t, w, on)
	if after := f.trunk(t); after == before {
		rest := p.rest()
		t.Fatalf("a push from the other clone did not move trunk, so this test cannot see one\nthe last request: %s", rest[len(rest)-1])
	}
}

// 🚨 The guard reads bash, and it reads every tool that is not first-party.
// Each other first-party tool gets no read. So each one must be a tool that
// runs no command and reaches no forge. A new first-party tool fails this
// test until a person puts its name in the list, or gives it to the rule.
func TestEachFirstPartyToolHasAPlaceInTheMergeRule(t *testing.T) {
	inert := []string{
		"read", "write", "edit", "grep", "glob",
		"terva_status", "session_inspect", "session_search", "session_list", "skill",
		"task_list", "task_create", "task_update", "task_archive", "memory", "activate_tools",
		"deliver_result", "chat_send_image", "chat_send_file", "share_file", "ask_user_question",
		"talkoot_send", "talkoot_handoff", "talkoot_roster", "talkoot_teams", "talkoot_templates",
		"talkoot_draft", "talkoot_note_write", "talkoot_note_read", "talkoot_propose",
		"talkoot_workspace", "talkoot_wake", "talkoot_routine",
		// A worktree is another directory of the same repository. The tools
		// make one and give it back, and they push nothing.
		"worktree_list", "worktree_create", "worktree_claim", "worktree_release", "worktree_remove",
		// The ticket tools write files in the store, and they push nothing.
		"ticket_list", "ticket_search", "ticket_get", "ticket_ready", "ticket_check", "ticket_create",
		"ticket_update", "ticket_transition", "ticket_claim", "ticket_comment", "ticket_fix",
		"ticket_store", "ticket_init",
		"world_note", "world_reveal", "actor_spawn", "raati_convene",
		// The script engine has read, grep and glob, and the second one has
		// write and edit. Neither has bash.
		"code_execution", "code_execution_mutating",
		// 🔑 A child of swarm_spawn has a gate of its own. The daemon marks
		// a child of unattended work, and that gate then holds this rule
		// (UnattendedChildRungs). The tool refuses a backend in such a
		// turn, because an external worker has no terva gate.
		"swarm_spawn",
	}
	for _, name := range permissions.BuiltinNames() {
		if name != "bash" && !slices.Contains(inert, name) {
			t.Errorf("the first-party tool %s has no place in the merge rule: the guard does not read it, and no person said that it runs no command and reaches no forge", name)
		}
	}
}

// A person's patterns add to the rule: a recipe that the rule does not know,
// and a branch that is not trunk.
func TestThePersonsPatternsBindAnUnattendedRun(t *testing.T) {
	seq := []string{
		"just land 12",
		"git commit -q --allow-empty -m work && git push origin HEAD:release",
	}
	p := &runScript{reply: "Done.", seq: seq}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	routinesConfig(t, `{"max": 5, "merge_commands": ["^just land\\b"], "protected_branches": ["release", "release/*"]}`)
	before := f.trunk(t)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)

	bodies := p.runs()
	last := bodies[len(bodies)-1]
	for _, want := range []string{"it matches a merge command of the user configuration", "its push moves the protected branch release"} {
		if !strings.Contains(last, want) {
			t.Errorf("the run did not read %q", want)
		}
	}
	if after := f.trunk(t); after != before {
		t.Fatal("trunk moved")
	}
	release := func() bool {
		return exec.Command("git", "--git-dir", f.bare, "rev-parse", "--verify", "-q", "refs/heads/release").Run() == nil
	}
	if release() {
		t.Fatal("the forge holds a release branch")
	}
	// 🚨 The proof that this test can see both. In a chain that a person
	// began the same two commands run: the recipe moves trunk, and the push
	// makes the release branch.
	for i, command := range seq {
		on := fmt.Sprintf("Do step %d.", i)
		p.set(func() { p.memberBash, p.memberBashOn = string(mustJSON(t, map[string]string{"command": command})), on })
		tell(t, w, on)
	}
	if after := f.trunk(t); after == before {
		t.Fatal("the person's recipe did not move trunk in a chain that a person began, so this test cannot see it run")
	}
	if !release() {
		t.Fatal("the push did not make the release branch in a chain that a person began, so this test cannot see it run")
	}
}

// repoFacts reads the branch at work, each remote's default branch, and the
// three settings that decide where a push with no branch goes.
func TestTheMergeRuleReadsTheRepository(t *testing.T) {
	cwd := testsupport.TempDir(t)
	newForge(t, cwd)
	want := func(what string, got, want permissions.RepoFacts) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %+v, want %+v", what, got, want)
		}
	}
	// git names the top level by its real path, which a test directory
	// behind a link is not.
	root := filepath.ToSlash(gitOut(t, cwd, "rev-parse", "--show-toplevel"))
	base := permissions.RepoFacts{Branch: "feat/x", Defaults: []string{"trunk"}, Upstream: "trunk", Root: root}
	want("a topic branch that follows trunk", repoFacts(t.Context(), cwd), base)
	// The same answer from a directory inside the repository.
	want("a directory inside the repository", repoFacts(t.Context(), filepath.Join(cwd, "scripts")), base)

	// 🚨 The rule reads those facts. A line that changes to the top level
	// stays in the repository, and a line that changes to another directory
	// leaves it.
	reads := func(what, command string, refused bool) {
		t.Helper()
		args := mustJSON(t, map[string]string{"command": command})
		why := permissions.MergeRule{}.Match("bash", args, func() permissions.RepoFacts { return repoFacts(t.Context(), cwd) })
		if (why != "") != refused {
			t.Fatalf("%s: %q gave %q", what, command, why)
		}
	}
	reads("cd to the top level", "cd "+root+" && git push origin HEAD", false)
	reads("cd to another directory", "cd "+filepath.ToSlash(filepath.Dir(root))+" && git push origin HEAD", true)

	// 🚨 With branch.autoSetupMerge a new branch follows the branch that it
	// starts at, and a push of it with one name goes there in the upstream
	// mode.
	gitOut(t, cwd, "config", "push.default", "upstream")
	reads("a new branch that follows nothing", "git checkout -b fix/y && git push -u origin fix/y", false)
	// git refuses a word that it does not know here too, for every command.
	gitOut(t, cwd, "config", "branch.autoSetupMerge", "Inherit")
	want("a word that git does not know", repoFacts(t.Context(), cwd), permissions.RepoFacts{Unknown: true})
	gitOut(t, cwd, "config", "branch.autoSetupMerge", "inherit")
	inherits := base
	inherits.PushDefault, inherits.AutoSetupMerge = "upstream", "inherit"
	want("branch.autoSetupMerge", repoFacts(t.Context(), cwd), inherits)
	reads("a new branch that follows its start", "git checkout -b fix/y && git push -u origin fix/y", true)
	// git reads true in any case, and it is the default.
	gitOut(t, cwd, "config", "branch.autoSetupMerge", "True")
	inherits.AutoSetupMerge = "true"
	want("branch.autoSetupMerge in capitals", repoFacts(t.Context(), cwd), inherits)
	reads("a new branch with the default", "git checkout -b fix/y && git push -u origin fix/y", false)
	gitOut(t, cwd, "config", "--unset", "branch.autoSetupMerge")
	gitOut(t, cwd, "config", "--unset", "push.default")

	// 🚨 git refuses a value of push.default that it does not know, for
	// every command. The repository is then unknown, and it is not read as
	// a repository with the default mode.
	gitOut(t, cwd, "config", "push.default", "Upstream")
	want("a mode that git does not know", repoFacts(t.Context(), cwd), permissions.RepoFacts{Unknown: true})

	gitOut(t, cwd, "config", "push.default", "upstream")
	up := base
	up.PushDefault = "upstream"
	want("push.default", repoFacts(t.Context(), cwd), up)

	gitOut(t, cwd, "config", "remote.origin.push", "refs/heads/*:refs/heads/*")
	up.PushRefspecs = true
	want("a remote with a refspec of its own", repoFacts(t.Context(), cwd), up)

	// 🚨 A push to a mirror moves every ref, with no --mirror in the
	// command. The mark reads as a refspec of the remote's own.
	gitOut(t, cwd, "config", "--unset", "remote.origin.push")
	gitOut(t, cwd, "config", "remote.origin.mirror", "false")
	up.PushRefspecs = false
	want("a remote that is no mirror", repoFacts(t.Context(), cwd), up)
	gitOut(t, cwd, "config", "remote.origin.mirror", "true")
	up.PushRefspecs = true
	want("a remote that is a mirror", repoFacts(t.Context(), cwd), up)

	gitOut(t, cwd, "commit", "-q", "--allow-empty", "-m", "work")
	gitOut(t, cwd, "checkout", "-q", "--detach")
	up.Branch, up.Upstream = "", ""
	want("a detached HEAD has no branch", repoFacts(t.Context(), cwd), up)

	// 🚨 A directory that git cannot read is unknown. The rule then refuses
	// a push that names no branch.
	want("a directory that is no repository", repoFacts(t.Context(), testsupport.TempDir(t)), permissions.RepoFacts{Unknown: true})
	// A repository with no setting that places a push. git config ends with
	// status 1 there, and that is an empty answer.
	plain := testsupport.TempDir(t)
	gitOut(t, plain, "init", "-q", "-b", "trunk")
	gitOut(t, plain, "commit", "-q", "--allow-empty", "-m", "one")
	want("a repository with no setting", repoFacts(t.Context(), plain), permissions.RepoFacts{
		Branch: "trunk", Root: filepath.ToSlash(gitOut(t, plain, "rev-parse", "--show-toplevel")),
	})

	// A bare repository has no top level, and the rule then reads each cd
	// as a way out.
	bare := filepath.Join(testsupport.TempDir(t), "bare.git")
	gitOut(t, plain, "clone", "-q", "--bare", plain, bare)
	want("a bare repository", repoFacts(t.Context(), bare), permissions.RepoFacts{Branch: "trunk", Defaults: nil, Upstream: ""})

	// 🚨 Any other failure of that read is no answer. A read that failed
	// must not stand for the default mode, in which a push with no branch
	// goes to a branch of the same name.
	was := repoConfigArgs
	t.Cleanup(func() { repoConfigArgs = was })
	repoConfigArgs = []string{"config", "--no-such-option"}
	want("a read of the settings that failed", repoFacts(t.Context(), plain), permissions.RepoFacts{Unknown: true})
	repoConfigArgs = was

	// A repository with no commit has a HEAD that names nothing yet.
	unborn := testsupport.TempDir(t)
	gitOut(t, unborn, "init", "-q", "-b", "trunk")
	want("a repository with no commit", repoFacts(t.Context(), unborn), permissions.RepoFacts{Unknown: true})
}
