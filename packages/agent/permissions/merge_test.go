package permissions

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func bashCall(t *testing.T, command string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// topic is a repository on a topic branch that follows trunk, as just
// scratch leaves a worktree of this repository.
var topic = RepoFacts{Branch: "feat/x", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}

func factsOf(f RepoFacts) func() RepoFacts { return func() RepoFacts { return f } }

func TestACommandThatMergesIsAMergeAction(t *testing.T) {
	for _, command := range []string{
		// A forge's CLI.
		"gh pr merge 12 --squash",
		"gh -R owner/repo pr merge 12",
		"gh pr merge --auto 12",
		"tea pulls merge --style rebase 12",
		"tea pr m 12",
		"tea pull merge 12",
		"glab mr merge 12",
		"glab mr accept 12",
		"fj pr merge 12",
		"/usr/local/bin/gh pr merge 12",
		// 🚨 An option and its value between the two words.
		"gh pr --repo owner/repo merge 12",
		"gh pr -R owner/repo merge 12 --squash",
		"gh -R owner/repo pr --repo owner/repo merge 12",
		"tea pulls --login bot --repo o/r merge 12",
		"glab mr -R group/project merge 12",
		// An option with no value: the next word is the subcommand.
		"gh pr --web merge 12",
		`"gh" 'pr' m\erge 12`,
		// A script and a recipe.
		"./scripts/pr.sh merge 12",
		"bash scripts/pr.sh merge 12 --style merge",
		"just pr-merge 12",
		"just --justfile ../justfile pr-merge 12",
		"make pr-merge PR=12",
		"npm run merge-pr",
		"pr-merge 12",
		// A forge's API.
		"curl -X POST https://forge.example/api/v1/repos/o/r/pulls/12/merge -d '{\"Do\":\"merge\"}'",
		`curl -X PUT "$API/repos/o/r/pulls/$n/merge"`,
		"tea api -X POST /repos/o/r/pulls/12/merge",
		"gh api -X PUT repos/o/r/pulls/12/merge",
		"glab api -X PUT projects/1/merge_requests/12/merge",
		"curl -X POST https://api.bitbucket.org/2.0/repositories/w/r/pullrequests/12/merge",
		`gh api graphql -f query='mutation { mergePullRequest(input: {pullRequestId: "x"}) { clientMutationId } }'`,
		// The same commands behind a runner, in a line, and in a line in a line.
		"sudo -u bot gh pr merge 12",
		"env GH_TOKEN=x timeout 30 gh pr merge 12",
		// 🚨 An option of a runner that takes a value, and a runner behind a
		// runner. Each once hid the program from the rule.
		"sudo -u bot just pr-merge 12",
		"env timeout 30 just pr-merge 12",
		"nice -n 5 ionice -c 3 make pr-merge",
		"timeout -s KILL 30 ./scripts/pr.sh merge 12",
		"sudo -u bot -- just pr-merge 12",
		// The value of the option is the name of a program that the rule knows.
		"sudo -u git just pr-merge 12",
		"env -u GH_TOKEN nohup just pr-merge 12",
		// An option that the rule does not know: the program is found later.
		"sudo --no-such-option value just pr-merge 12",
		"echo 12 | xargs -I{} just pr-merge {}",
		"git status && gh pr merge 12",
		"echo done; (cd /tmp && just pr-merge 12)",
		"echo $(gh pr merge 12)",
		"sh -c 'gh pr merge 12'",
		"bash -lc \"just pr-merge 12\"",
		"eval gh pr merge 12",
		"sudo sh -c 'tea pulls merge 12'",
		"gh api -X POST repos/o/r/merges -f base=main -f head=feat/x",
		// A line that a shell reads from its input.
		"bash <<'EOF'\ngh pr merge 12\nEOF",
		"sh <<EOF\njust pr-merge 12\nEOF",
		"bash <<< 'tea pulls merge 12'",
		"sudo bash -s <<'EOF'\ngit status\ngh pr merge 12\nEOF",
		// A line that the parser refuses.
		"gh pr merge 12 && (",
		// 🚨 The same for a line inside a line. The shell runs its front
		// before it meets the error.
		"sh -c 'gh pr merge 12 && ('",
		"bash <<'EOF'\ngh pr merge 12\nif then (\nEOF",
		"eval 'tea pulls merge 12; ('",
		// 🚨 An option of the shell that takes a value, before the line.
		"bash -o pipefail -c 'gh pr merge 12'",
		"bash -e -o errexit -O extglob -c 'just pr-merge 12'",
		"bash --rcfile /dev/null -c 'tea pulls merge 12'",
		"sh +o posix -c 'gh pr merge 12'",
		// 🚨 Options of the shell in one word, and an option after -c. The
		// shell reads its options first, and then the line.
		"bash -euo pipefail -c 'gh pr merge 12'",
		"bash -c -e 'gh pr merge 12'",
		"bash -c -o pipefail 'just pr-merge 12'",
		"bash -xeuo pipefail -O extglob -c 'tea pulls merge 12' arg0",
		"bash -euo pipefail scripts/pr.sh merge 12",
		"bash -- scripts/pr.sh merge 12",
		// 🚨 A shell at the end of a pipe runs what the pipe gives it.
		"echo 'gh pr merge 12' | sh",
		"printf 'just pr-merge 12\\n' | bash",
		"cat <<'EOF' | bash\ngh pr merge 12\nEOF",
		"echo 'tea pulls merge 12' | sudo bash -s",
		"echo 'gh pr merge 12' |& bash -e",
		"echo 'gh pr merge 12' | bash -euo pipefail",
		// 🚨 A shell in the middle of a pipe reads the pipe too.
		"echo 'gh pr merge 12' | bash | tee out.log",
		"echo 'just pr-merge 12' | tee plan.txt | sh | cat",
		"cat plan.txt | sed s/x/y/ | bash -s | tee out.log | wc -l; echo pr merge",
		// 🚨 A program that prints its words is read as every other one is,
		// because the shell can run what it printed. The cost is a refusal of
		// echo pr merge, and quotes around the two words end it.
		"$(echo gh pr merge 12)",
		"`echo gh pr merge 12`",
		`eval "$(printf '%s ' gh pr merge 12)"`,
		"cmd=$(echo gh pr merge 12); $cmd",
		"echo pr merge 12 | xargs gh",
		// An alias holds a line.
		"alias land='gh pr merge 12'; land",
		// 🚨 A line inside more shells than the rule reads is refused.
		`sh -c "sh -c \"sh -c 'sh -c \\\"sh -c true\\\"'\""`,
		"bash bash bash bash bash bash x",
	} {
		if why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic)); why == "" {
			t.Errorf("the rule let a merge pass: %s", command)
		}
	}
}

func TestACommandThatMergesNothingPasses(t *testing.T) {
	for _, command := range []string{
		"gh pr create --title 'a fix' --body 'see the notes'",
		"gh pr view 12",
		"gh pr comment 12 --body 'do not pr merge this yet'",
		"gh pr list --search merge",
		"gh pr --repo owner/repo list --search merge",
		"gh pr --repo owner/repo view 12 --json mergeable",
		"gh pr comment 12 --body merge",
		// An option that holds its value takes no word: checkout is the
		// subcommand, and merge is the name of a branch.
		"gh pr --repo=owner/repo checkout merge",
		"gh pr --repo owner/repo checks 12",
		"tea pulls create --title x",
		"just pr -t 'fix: a thing' -F .tmp/body.md",
		"just pr-status 12",
		"just pr-review 12",
		"git merge origin/sothr-main",
		"git pull --rebase origin sothr-main",
		"git pull origin merge-fix",
		"git commit -m 'docs: how gh pr merge works'",
		"grep -rn pr-merge justfile",
		"cat docs/plans/forgejo-workflow.md",
		"curl https://forge.example/api/v1/repos/o/r/pulls/12",
		"curl https://forge.example/api/v1/repos/o/r/pulls/12/merged-by",
		"git log --merges",
		"go test ./...",
		// A runner in front of a command that merges nothing.
		"timeout 300 go test ./...",
		"sudo -u bot gh pr view 12",
		"git ls-files | xargs grep -n pr-merge",
		"env GOFLAGS=-mod=mod nice -n 5 go build ./...",
		"sh -c 'go test ./... && ('",
		"bash -o pipefail -c 'go test ./... | tee out.log'",
		"echo 'go test ./...' | sh",
		"git log --oneline | bash scripts/count.sh",
		// A pipe into a program that is no shell, and into a shell that
		// runs a script: the text in the pipe is no command.
		"echo 'a person runs gh pr merge 12' | tee notes.md",
		"grep -rn 'pr merge' docs | head -n 5",
		"echo 'gh pr merge 12' | bash scripts/lint-text.sh",
		"echo 'gh pr merge 12' | bash -euo pipefail scripts/lint-text.sh | tee out.log",
		// The first stage of a pipe reads no pipe.
		"sh | grep -c 'pr merge'",
		"bash -euo pipefail -c 'go test ./...'",
		"bash -c -e 'go vet ./...'",
		"export GOFLAGS=-mod=mod && go build ./...",
		"alias t='go test ./...'; t",
		// A here document that no shell reads is text.
		"cat > notes.md <<'EOF'\nrun gh pr merge 12 when it is green\nEOF",
		"bash <<'EOF'\ngo test ./...\nEOF",
	} {
		if why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic)); why != "" {
			t.Errorf("the rule refused %q: %s", command, why)
		}
	}
}

func TestAPushOfTheRunsOwnBranchPasses(t *testing.T) {
	for _, command := range []string{
		"git push -u origin feat/x",
		"git push origin feat/x",
		"git push origin HEAD",
		"git push origin HEAD:refs/heads/feat/x",
		"git push --force-with-lease origin feat/x",
		"git push origin +feat/x",
		"git push origin feat/x:feat/y",
		`git push -u origin "$(git branch --show-current)"`,
		"git push origin $(git rev-parse --abbrev-ref HEAD)",
		"git push origin tag v1.2.3",
		"git push origin refs/tags/v1.2.3",
		"git push origin HEAD:refs/for/sothr-main",
		"git push -o ci.skip origin feat/x",
		"git push",
		"git push origin",
		"git add -A && git commit -m 'a fix' && git push",
		"git branch --show-current && git push",
		"git config --get push.default; git push",
		"git remote -v && git push",
		"git checkout -- a-file.go && git push",
		// A new branch and its push on one line. The branch is named, and
		// nothing on the line moves the remote.
		"git checkout -b feat/y && git push -u origin feat/y",
		"git switch -c feat/y && git commit -m x && git push origin feat/y",
		// A setting that places no push.
		"git config user.name helm && git push origin feat/x",
		"git config --global core.editor vi && git push",
		"git config --file .git/config user.name helm && git push",
		// A read of a setting that places a push changes nothing.
		"git config --get remote.origin.url && git push -u origin HEAD",
		"git config get push.default && git push",
		"git config list --show-origin && git push origin HEAD",
		"git config --get-regexp '^branch\\.' && git push",
		"cd ../other && git push origin HEAD:refs/heads/feat/x",
		"git push --repo=origin feat/x",
		"timeout 300 go test ./... && git push origin feat/x",
		"bash -o pipefail -c 'go test ./... && git push origin feat/x'",
		"export GOFLAGS=-mod=mod && git push origin feat/x",
		"git --no-pager push origin feat/x",
		"git -P --no-optional-locks push",
		"bash -euo pipefail -c 'go test ./... && git push origin feat/x'",
		"GOFLAGS=-mod=mod git push",
	} {
		if why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic)); why != "" {
			t.Errorf("the rule refused %q: %s", command, why)
		}
	}
}

func TestAPushThatMovesAProtectedBranchIsAMergeAction(t *testing.T) {
	for _, command := range []string{
		"git push origin sothr-main",
		"git push origin HEAD:sothr-main",
		"git push origin HEAD:refs/heads/sothr-main",
		"git push origin feat/x:sothr-main",
		"git push origin +feat/x:sothr-main",
		"git push --force origin feat/x:heads/sothr-main",
		"git push origin feat/x main",
		"git push origin master",
		"git push origin trunk",
		"git push origin :sothr-main",
		"git push origin --delete sothr-main",
		"git push -o ci.skip origin sothr-main",
		"/usr/bin/git push origin sothr-main",
		"sudo git push origin sothr-main",
		"sudo -u bot git push origin HEAD:sothr-main",
		"env timeout 30 git push origin sothr-main",
		"nice -n 5 git push origin HEAD:main",
		"sudo --no-such-option value git push origin HEAD:main",
		// 🚨 --repo names the remote, so every plain word is a refspec.
		"git push --repo=origin HEAD:sothr-main",
		"git push --repo origin sothr-main",
		"git push --repo=origin feat/x:refs/heads/main",
		"git add -A && git commit -m x && git push origin HEAD:sothr-main",
		"sh -c 'git push origin HEAD:sothr-main'",
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic))
		if !strings.Contains(why, "protected branch") {
			t.Errorf("want a refusal that names the protected branch for %q, got %q", command, why)
		}
	}
}

func TestAPushOfMoreThanItNamesIsAMergeAction(t *testing.T) {
	for _, command := range []string{
		"git push --all origin",
		"git push --mirror",
		"git push origin --branches",
		"git push origin :",
		"git push origin 'refs/heads/*:refs/heads/*'",
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic))
		if !strings.Contains(why, "more branches than it names") {
			t.Errorf("want a refusal of a push of every branch for %q, got %q", command, why)
		}
	}
}

func TestAPushThatTheRuleCannotReadIsAMergeAction(t *testing.T) {
	for _, command := range []string{
		// A word that holds a variable.
		`git push origin "$branch"`,
		"git push origin HEAD:$target",
		"git push $(cat remote.txt) feat/x",
		// The branch at work is not the branch that the host read.
		"git checkout sothr-main && git push",
		"git switch sothr-main; git push origin HEAD",
		"cd ../other && git push",
		"git branch -u origin/sothr-main && git push",
		"git config push.default upstream && git push",
		// 🚨 An option of git config that takes a word stands in front of
		// the key. The key is found wherever it stands.
		"git config --file .git/config push.default upstream && git push",
		"git config -f .git/config --replace-all push.default upstream && git push",
		"git config --type=bool --file .git/config remote.origin.mirror true && git push origin HEAD",
		"git config set --file .git/config push.default upstream && git push",
		"git config --comment 'by helm' branch.feat/x.merge refs/heads/main && git push",
		// A file with the name get or --get does not make the write a read.
		"git config --file get push.default upstream && git push",
		"git config --file --get push.default upstream && git push",
		"git config --rename-section push pushed && git push",
		"git config --local --edit && git push",
		"git config include.path ../other.gitconfig && git push",
		`git config "$key" upstream && git push`,
		"git remote set-url origin elsewhere && git push",
		// git reads another repository or another configuration.
		"git -C ../other push",
		"git -c push.default=upstream push",
		"git --git-dir=../other/.git push",
		"GIT_DIR=../other/.git git push",
		"env GIT_CONFIG_COUNT=1 git push",
		"git send-pack origin feat/x",
		"bash <<'EOF'\ngit checkout main\ngit push\nEOF",
		"bash -o errexit -c 'git checkout main && git push'",
		"printf 'git push origin HEAD:main\\n' | bash",
		// 🚨 git takes the value of -c and of -C in the same word. Any option
		// of git that the rule does not know is a doubt.
		"git -cpush.default=upstream push",
		"git -C../other push",
		"git --exec-path=/elsewhere push origin feat/x:feat/x",
		"git --no-such-option push origin feat/x:feat/x",
		// The directory of the person's configuration is git's too.
		"HOME=/tmp/other git push",
		"env XDG_CONFIG_HOME=/tmp/other git push",
		"export HOME=/tmp/other; git push origin HEAD",
		// 🚨 An exported variable of git's own moves every push after it.
		"export GIT_DIR=../other/.git && git push",
		`export GIT_CONFIG_PARAMETERS="'push.default=upstream'" && git push`,
		"declare -x GIT_WORK_TREE=/elsewhere; git push origin HEAD",
		"export GIT_DIR=../other/.git; git push origin feat/x:feat/x",
		// xargs adds words that the rule cannot see.
		"echo main | xargs git push origin",
		"echo HEAD:main | xargs -n 1 git push origin feat/x",
		"sh -c 'git push origin feat/x && ('",
		// A refspec with one side, where the remote's own refspec may send it.
		"cd ../other && git push origin feat/x",
		"git config remote.origin.push refs/heads/feat/x:refs/heads/main && git push origin feat/x",
		"git remote add fork elsewhere && git push fork feat/x",
		// A line that the parser refuses.
		"git push origin feat/x && (",
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic))
		if !strings.Contains(why, "cannot read where its push goes") {
			t.Errorf("want a refusal of a push that cannot be read for %q, got %q", command, why)
		}
	}
}

// A push that names no branch goes where git's configuration sends it.
// just scratch gives a topic branch the upstream origin/sothr-main, so the
// mode of push.default decides whether that push moves trunk.
func TestAPushWithNoBranchFollowsTheConfiguration(t *testing.T) {
	onTrunk := RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}
	for _, tc := range []struct {
		name    string
		command string
		facts   RepoFacts
		want    string
	}{
		{"simple, on a topic branch", "git push", topic, ""},
		{"current, on a topic branch", "git push", with(topic, "current"), ""},
		{"nothing", "git push", with(topic, "nothing"), ""},
		{"upstream, and the upstream is trunk", "git push", with(topic, "upstream"), "protected branch sothr-main"},
		{"tracking is the old name of upstream", "git push", with(topic, "tracking"), "protected branch sothr-main"},
		{"matching", "git push", with(topic, "matching"), "more branches than it names"},
		{"a mode that the rule does not know", "git push", with(topic, "later"), "cannot read where its push goes"},
		{"simple, on trunk", "git push", onTrunk, "protected branch sothr-main"},
		{"HEAD, on trunk", "git push origin HEAD", onTrunk, "protected branch sothr-main"},
		{"the branch at work, on trunk", `git push origin "$(git branch --show-current)"`, onTrunk, "protected branch sothr-main"},
		{"a remote with a refspec of its own", "git push", RepoFacts{Branch: "feat/x", PushRefspecs: true}, "cannot read where its push goes"},
		// 🚨 Such a refspec also sends a branch that the push names with one
		// side. git push origin feat/x moves trunk when the remote maps it.
		{"one side, and the remote has a refspec", "git push origin feat/x", RepoFacts{Branch: "feat/x", PushRefspecs: true}, "cannot read where its push goes"},
		{"HEAD, and the remote has a refspec", "git push origin HEAD", RepoFacts{Branch: "feat/x", PushRefspecs: true}, "cannot read where its push goes"},
		{"both sides, and the remote has a refspec", "git push origin feat/x:feat/x", RepoFacts{Branch: "feat/x", PushRefspecs: true}, ""},
		{"both sides to trunk, and the remote has a refspec", "git push origin feat/x:main", RepoFacts{Branch: "feat/x", PushRefspecs: true}, "protected branch main"},
		// --tags is a refspec of its own, so push.default adds no branch to
		// it. --follow-tags is none, and the branch goes too.
		{"the tags alone, and the upstream is trunk", "git push origin --tags", with(topic, "upstream"), ""},
		{"the tags alone, with no remote", "git push --tags", with(topic, "upstream"), ""},
		{"the tags alone, on trunk", "git push origin --tags", onTrunk, ""},
		{"the tags and a branch", "git push origin --tags sothr-main", topic, "protected branch sothr-main"},
		{"the branch and its tags", "git push origin --follow-tags", with(topic, "upstream"), "protected branch sothr-main"},
		// 🚨 At a mirror remote a push of the tags deletes each branch that
		// this repository has not. The host reads a mirror as a refspec.
		{"the tags alone, and the remote has a refspec", "git push origin --tags", RepoFacts{Branch: "feat/x", PushRefspecs: true}, "cannot read where its push goes"},
		{"the tags alone in a repository that the host cannot read", "git push origin --tags", RepoFacts{Unknown: true}, "cannot read where its push goes"},
		{"the tags alone in another directory", "cd ../other && git push origin --tags", topic, "cannot read where its push goes"},
		// 🚨 With no branch at work a rebase or a bisect has not ended. A
		// command in front of the push can end it on a protected branch.
		{"no branch at work", "git push", RepoFacts{}, "cannot read where its push goes"},
		{"no branch at work, and a rebase that ends", "git rebase --continue && git push origin HEAD", RepoFacts{}, "cannot read where its push goes"},
		{"no branch at work, and both sides", "git push origin HEAD:refs/heads/feat/x", RepoFacts{}, ""},
		{"an upstream on another branch", "git push", with(RepoFacts{Branch: "feat/x", Upstream: "feat/x"}, "upstream"), ""},
		// 🚨 A repository that the host could not read is not a repository
		// with no branch. Only a push that names its branch passes.
		{"a repository that the host cannot read", "git push", RepoFacts{Unknown: true}, "cannot read where its push goes"},
		{"HEAD in such a repository", "git push origin HEAD", RepoFacts{Unknown: true}, "cannot read where its push goes"},
		{"a branch with both sides in such a repository", "git push origin feat/x:feat/x", RepoFacts{Unknown: true}, ""},
		{"a branch with one side in such a repository", "git push origin feat/x", RepoFacts{Unknown: true}, "cannot read where its push goes"},
		{"trunk in such a repository", "git push origin main", RepoFacts{Unknown: true}, "protected branch main"},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, tc.command), factsOf(tc.facts))
		if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
			t.Errorf("%s: %q gave %q, want %q", tc.name, tc.command, why, tc.want)
		}
	}
}

func with(f RepoFacts, pushDefault string) RepoFacts {
	f.PushDefault = pushDefault
	return f
}

func TestTheRuleAsksTheRepositoryOnlyForAPush(t *testing.T) {
	asked := 0
	facts := func() RepoFacts { asked++; return topic }
	for _, command := range []string{"go test ./...", "gh pr merge 12", "git status", "git commit -m x"} {
		(MergeRule{}).Match("bash", bashCall(t, command), facts)
	}
	if asked != 0 {
		t.Fatalf("the rule read the repository %d times for commands with no push", asked)
	}
	(MergeRule{}).Match("bash", bashCall(t, "git push && git push origin feat/x"), facts)
	if asked != 1 {
		t.Fatalf("the rule read the repository %d times for one line, want 1", asked)
	}
}

func TestAPersonsPatternsAddToTheRule(t *testing.T) {
	r := MergeRule{
		Commands:  []*regexp.Regexp{regexp.MustCompile(`^just land\b`), regexp.MustCompile(`deploy\.sh --prod`)},
		Protected: []string{"release", "release/*"},
	}
	for command, want := range map[string]string{
		"just land 12":                           "merge command of the user configuration",
		"git status && just land 12":             "merge command of the user configuration",
		"sh -c 'just land 12'":                   "merge command of the user configuration",
		"./deploy.sh --prod":                     "merge command of the user configuration",
		`"./deploy.sh" '--prod'`:                 "merge command of the user configuration",
		"git push origin HEAD:release":           "protected branch release",
		"git push origin feat/x:release/v0.142":  "protected branch release/v0.142",
		"git push origin feat/x:releases":        "",
		"just landing-page":                      "",
		"git push origin feat/x:refs/heads/main": "protected branch main",
	} {
		why := r.Match("bash", bashCall(t, command), factsOf(topic))
		if (want == "") != (why == "") || !strings.Contains(why, want) {
			t.Errorf("%q gave %q, want %q", command, why, want)
		}
	}
	// The same commands pass a rule with no pattern of the person's.
	for _, command := range []string{"just land 12", "./deploy.sh --prod", "git push origin HEAD:release"} {
		if why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic)); why != "" {
			t.Errorf("a rule with no pattern refused %q: %s", command, why)
		}
	}
}

func TestAToolOfAnExtensionThatMergesIsAMergeAction(t *testing.T) {
	for tool, args := range map[string]string{
		"mcp_github_merge_pull_request":    `{"owner":"o","repo":"r","pullNumber":12}`,
		"mcp_gitlab_merge_merge_request":   `{"iid":12}`,
		"mcp_forgejo_pr_merge":             `{"index":12}`,
		"mcp_gitea_merge_branch":           `{"head":"feat/x","base":"main"}`,
		"mcp_fetch_fetch":                  `{"url":"https://forge.example/api/v1/repos/o/r/pulls/12/merge","method":"POST"}`,
		"mcp_github_enable_pr_automerge":   `{"n":12}`,
		"forge_request":                    `{"path":"/repos/o/r/pulls/12/merge"}`,
		"mcp_github_graphql":               `{"query":"mutation { mergePullRequest(input: {}) { clientMutationId } }"}`,
		"mcp_bitbucket_merge_pull_request": `{}`,
		// 🚨 An endpoint inside an object or a list.
		"mcp_http_request": `{"request":{"method":"POST","url":"https://forge.example/api/v1/repos/o/r/pulls/12/merge"}}`,
		"mcp_http_batch":   `{"requests":[{"url":"https://forge.example/x"},{"url":"https://forge.example/api/v1/repos/o/r/pulls/12/merge"}]}`,
		// 🚨 A tool that runs a command line.
		"mcp_shell_execute": `{"command":"gh pr merge 12"}`,
		"mcp_shell_run":     `{"cmd":"just pr-merge 12","cwd":"/work"}`,
		"mcp_exec_spawn":    `{"command":["tea","pulls","merge","12"]}`,
		"mcp_term_send":     `{"session":1,"input":{"script":"git push origin HEAD:main"}}`,
		// The host does not know such a tool's directory, so a push that
		// names no branch cannot be read.
		"mcp_shell_exec": `{"command":"git push"}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), nil); why == "" {
			t.Errorf("the rule let %s %s pass", tool, args)
		}
	}
	for tool, args := range map[string]string{
		"mcp_github_create_pull_request":        `{"title":"merge the two readers"}`,
		"mcp_github_update_pull_request_branch": `{"pullNumber":12}`,
		"mcp_github_get_pull_request":           `{"pullNumber":12}`,
		"mcp_fetch_fetch":                       `{"url":"https://forge.example/api/v1/repos/o/r/pulls/12"}`,
		"mcp_git_merge_base":                    `{"a":"x","b":"y"}`,
		"mcp_shell_execute":                     `{"command":"go test ./..."}`,
		"mcp_shell_run":                         `{"cmd":"git push origin HEAD:refs/heads/feat/x"}`,
		"mcp_notes_add":                         `{"text":"run gh pr merge 12 when it is green","tags":["merge"]}`,
		// A first-party tool merges nothing, whatever its text says.
		"write":        `{"path":"docs/forge.md","content":"POST /repos/o/r/pulls/12/merge merges a pull request"}`,
		"edit":         `{"path":"justfile","old":"pr-merge","new":"pr-merge:"}`,
		"grep":         `{"pattern":"/pulls/[0-9]+/merge"}`,
		"talkoot_send": `{"to":["human"],"body":"please run just pr-merge 12"}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), nil); why != "" {
			t.Errorf("the rule refused %s %s: %s", tool, args, why)
		}
	}
}

// The routes of an independent security read of 8f92d0e2b. Each line passed
// the rule then. The read proved each one against a real remote or a stub of
// the forge's CLI.
func TestTheRuleReadsWhatALineHides(t *testing.T) {
	const (
		merges    = "merge"
		protected = "protected branch"
		unread    = "cannot read where its push goes"
		every     = "more branches than it names"
	)
	up := with(topic, "upstream")
	own := with(RepoFacts{Branch: "feat/x", Defaults: []string{"sothr-main"}, Upstream: "feat/x"}, "upstream")
	for _, tc := range []struct {
		command string
		facts   RepoFacts
		want    string
	}{
		// 🚨 In the upstream mode a refspec with one side goes to the
		// upstream of its branch. A branch of just scratch follows trunk.
		{"git push origin feat/x", up, protected},
		{`git push -u origin "$(git branch --show-current)"`, up, protected},
		{"git push --force-with-lease origin feat/x", up, protected},
		// git does that for the name of a branch. HEAD and @ go to the
		// branch of the same name, and a probe with a real remote said so.
		{"git push origin HEAD", up, ""},
		{"git push -u origin HEAD", up, ""},
		{"git push origin @", up, ""},
		{"git push origin HEAD", with(RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}, "upstream"), protected},
		{"git push origin refs/heads/feat/x", up, protected},
		{"git push origin feat/y", up, unread},
		{"git branch -u origin/main && git push origin feat/x", own, unread},
		{"git push origin feat/x:feat/x", up, ""},
		{"git push origin HEAD:refs/heads/feat/x", up, ""},
		{"git push origin feat/x", own, ""},
		{"git push origin feat/x", with(RepoFacts{Branch: "feat/x"}, "upstream"), ""},
		{"git push origin feat/x", with(topic, "simple"), ""},

		// 🚨 A line inside a word that holds a variable.
		{`sh -c "git push origin HEAD:main; echo exit=$?"`, topic, protected},
		{`bash -c "gh pr merge $PR --squash"`, topic, merges},
		{`n=12; eval "gh pr merge $n"`, topic, merges},
		{"bash <<EOF\ngit push origin HEAD:main\necho $?\nEOF", topic, protected},
		{`bash $OPTS -c 'gh pr merge 12'`, topic, merges},
		// Quotes inside the line do not make the variable a plain text.
		{`sh -c "git push origin '$b'"`, topic, unread},
		{"bash <<EOF\ngit push origin '$b'\nEOF", topic, unread},
		{`sh -c "git push origin feat/x:feat/x; echo exit=$?"`, topic, ""},
		{`sh -c "go test ./... || echo failed=$?"`, topic, ""},

		// 🚨 A line that a variable of the same line holds.
		{`cmd="git push origin HEAD:main"; echo "$cmd"; eval "$cmd"`, topic, protected},
		{`cmd=(git push origin HEAD:main); "${cmd[@]}"`, topic, protected},
		{`CMD="gh pr merge 12 --squash"; $CMD`, topic, merges},
		{`export CMD='just pr-merge 12'; $CMD`, topic, merges},
		{`body="This pr merge waits for a person"; gh pr create --body "$body"`, topic, ""},
		{`msg="docs: how a push is read"; git commit -m "$msg" && git push origin feat/x:feat/x`, topic, ""},

		// 🚨 A program in front that the rule does not know.
		{"flock .git/push.lock git push origin HEAD:main", topic, protected},
		{"ssh-agent bash -c 'git push origin HEAD:main'", topic, protected},
		{"script -qc 'git push origin HEAD:main' /dev/null", topic, protected},
		{`find . -maxdepth 0 -exec git push origin HEAD:main \;`, topic, protected},
		{"busybox sh -c 'gh pr merge 12'", topic, merges},
		{"env -S 'git push origin HEAD:main'", topic, protected},
		{"trap 'git push origin HEAD:main' EXIT", topic, protected},
		{"ssh build 'cd repo && gh pr merge 12'", topic, merges},
		{"flock /tmp/lock just pr-merge 12", topic, merges},
		{"hub push origin HEAD:main", topic, protected},
		{`trap 'rm -rf "$tmp"' EXIT`, topic, ""},
		{"ssh build 'ls -la /var/log'", topic, ""},
		{`find . -name '*.sh' -exec bash -n {} \;`, topic, ""},
		{`grep -rn "gh pr merge" docs/`, topic, ""},
		{`git ls-files | xargs grep -n "gh pr merge"`, topic, ""},
		{`timeout 60 grep -rn "just pr-merge" docs/`, topic, ""},
		{"flock /tmp/lock go test ./...", topic, ""},

		// 🚨 A change of the branch at work, or of its upstream.
		{"git checkout main -- && git merge feat/x && git push", topic, unread},
		{"git switch -- main && git push", topic, unread},
		{"git rebase feat/x main && git push", topic, unread},
		{"git branch -Mf main && git push origin HEAD", topic, unread},
		{"git pull --set-upstream origin main && git push", own, unread},
		{"git fetch --set-upstream origin main && git push", own, unread},
		{"git branch -uorigin/main && git push", own, unread},
		{"git branch --set-upstream-t=origin/main && git push", own, unread},
		{"git checkout main -- a-file.go && git push", topic, ""},
		{"git rebase origin/sothr-main && git push", topic, ""},
		{"git pull --rebase origin sothr-main && git push", topic, ""},
		{"git fetch origin && git branch -d old && git push", topic, ""},

		// 🚨 A word that the shell makes more than one word of.
		{"git push origin HEAD:{main,feat/x}", topic, unread},
		{"git push origin {feat/x,main}", topic, unread},
		{`git push origin $'HEAD:ma\x69n'`, topic, unread},
		{"gh pr {merge,--squash} 12", topic, merges},
		// A variable in that place is most often a number, as tea takes it.
		{"gh pr $action 12", topic, ""},
		{"tea pr $PR", topic, ""},
		{`tea pulls "$PR"`, topic, ""},
		{"gh pr view $PR", topic, ""},
		{`gh pr --repo "$REPO" view 12`, topic, ""},
		{"docker pull $IMAGE", topic, ""},
		{"git push origin feat/x:feat/x HEAD@{0}:refs/heads/feat/y", topic, ""},

		// 🚨 git's own reading of its words.
		{"git config alias.p 'push origin HEAD:main' && git p", topic, protected},
		{"git -c alias.p='push origin HEAD:main' p", topic, protected},
		{"git -calias.land='!gh pr merge 12' land", topic, merges},
		{"git --attr-source HEAD push origin HEAD:main", topic, unread},
		{"git --attr-source HEAD checkout main && git push", topic, unread},
		{"git config --comment --get push.default upstream && git push", topic, unread},
		{"git tag tag && git push origin +tag HEAD:main", topic, protected},
		{"git push --al origin", topic, every},
		{"git push --mirr", topic, every},
		// --repo counts for nothing beside a word, so this is git push
		// origin. The rule reads the word as a branch too, and it knows the
		// upstream of no such branch.
		{"git push --repo=origin origin", up, unread},
		{"git push --repo=origin origin", topic, ""},
		{"git push --repo=origin origin", RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}}, protected},
		{"git push --rep origin HEAD:main", topic, protected},
		{"GIT_DIR=../other/.git sh -c 'git push'", topic, unread},
		{"GIT_DIR=../other/.git eval 'git push'", topic, unread},
		{"env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=push.default GIT_CONFIG_VALUE_0=upstream bash -c 'git push'", topic, unread},
		{"builtin export GIT_DIR=../other/.git; git push", topic, unread},
		{"export 'GIT_DIR=../other/.git' && git push", topic, unread},
		{"HOME=/tmp/other; git push", topic, unread},
		{`echo "push origin HEAD:main" | xargs git`, topic, unread},
		{`git "$sub" origin main`, topic, unread},
		{"git subtree push --prefix=sub origin main", topic, unread},
		{"git push -o merge_request.create -o merge_request.merge_when_pipeline_succeeds origin feat/x", topic, merges},
		{"git push -omerge_request.auto_merge origin feat/x:feat/x", topic, merges},
		{"git push -o ci.skip -o merge_request.create origin feat/x", topic, ""},
		// A variable of git's own that leaves a push where it was.
		{"GIT_TERMINAL_PROMPT=0 git push origin HEAD:refs/heads/feat/x", topic, ""},
		{"GIT_TRACE=1 GIT_AUTHOR_NAME=helm git push", topic, ""},
		{"git ls-files | xargs git add", topic, ""},

		// 🚨 A pipe into a shell.
		{"echo 'gh pr merge 12' | sh -", topic, merges},
		{"echo 'gh pr merge 12' | bash -s -- x", topic, merges},
		{"echo 'gh pr merge 12' | (sh)", topic, merges},
		{"echo 'gh pr merge 12' | { bash; }", topic, merges},
		{"echo 'gh pr -R o/r merge 12' | bash", topic, merges},
		{"echo 'glab mr accept 12' | sh", topic, merges},
		{"echo 'tea pr m 12' | sh", topic, merges},
		{"echo glab mr accept 12 | sh", topic, merges},
		{"echo ./scripts/pr.sh merge 12 | sh", topic, merges},
		{"(echo 'glab mr accept 12') | sh", topic, merges},
		{"cat <<'EOF' | bash\ngh pr -R o/r merge 12\nEOF", topic, merges},
		{"echo 'go test ./...' | sh -s", topic, ""},

		// Lines of ordinary work in this repository.
		{`GOENV=off GOTMPDIR="$PWD/.tmp/gotmp" just ci > .tmp/ci.log 2>&1; rc=$?; echo "ci rc=$rc"`, topic, ""},
		{`TICKET_ACTOR=agent:terva/mieli just pr -t "feat: a thing" -F .tmp/body.md`, topic, ""},
		{"tea api /repos/o/r/pulls/12/reviews > .tmp/reviews.json", topic, ""},
		{`git fetch --prune && git commit -s -m "fix(x): a thing" && git push -u origin feat/x`, topic, ""},
		{`for f in a.go b.go; do git add "$f"; done; git commit -m x; git push origin HEAD`, topic, ""},
		{`cd "$(git rev-parse --show-toplevel)" && just check`, topic, ""},
		{`gh pr create --title "a fix" --body "$(cat .tmp/body.md)"`, topic, ""},
		{`tea pulls create --title "a fix" --description "$(cat .tmp/body.md)"`, topic, ""},
		{"git stash push -m wip && git stash pop", topic, ""},
		{`echo "$out" | grep -c merge`, topic, ""},
		{"sh -c 'cd /work && go test ./...'", topic, ""},
		{"just pr-review 12 --panel code", topic, ""},
		{`git push origin HEAD:refs/heads/"$(git branch --show-current)"`, topic, ""},

		// A forge's endpoint with its quotes removed, and GitHub's other
		// mutations that merge.
		{"curl -X PUT https://forge.example/api/v1/repos/o/r/pulls/12/'merge'", topic, "merge endpoint"},
		{"gh api graphql -f query='mutation { enablePullRequestAutoMerge(input: {}) { clientMutationId } }'", topic, "merge endpoint"},
		{"gh api graphql -f query='mutation { enqueuePullRequest(input: {}) { clientMutationId } }'", topic, "merge endpoint"},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, tc.command), factsOf(tc.facts))
		if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
			t.Errorf("%q gave %q, want %q", tc.command, why, tc.want)
		}
	}
}

// 🚨 The rule runs in a session's gate, on a line that a model wrote. The
// parser nests a pipe of n stages n deep, and a read at each node took 20
// seconds for 500 stages.
func TestALongLineIsReadInTime(t *testing.T) {
	for name, command := range map[string]string{
		"a pipe of shells":         "echo x" + strings.Repeat(" | sh", 4000),
		"a pipe of other programs": "echo x" + strings.Repeat(" | cat", 15000),
		"a command of many names":  "echo" + strings.Repeat(" git", 20000),
		"subshells":                strings.Repeat("(", 2000) + "sh" + strings.Repeat(")", 2000),
		"shells in shells":         strings.Repeat("sh -c ", 2000) + "true",
		// ( with a space behind it. (( starts arithmetic, and such a line
		// does not parse.
		"pipes in groups":           strings.Repeat("( ", 4000) + "echo x" + strings.Repeat(" | sh )", 4000),
		"pipes in later groups":     "echo x" + strings.Repeat(" | ( cat", 4000) + strings.Repeat(" )", 4000),
		"pipes in substitutions":    strings.Repeat("echo $( ", 4000) + "true" + strings.Repeat(" | sh )", 4000),
		"lines in lines in a pipe":  linesInLines(32),
		"more groups than are read": strings.Repeat("(", 250000),
		// 🚨 The rule's own walk calls itself for each stage of a pipe and
		// each command of a chain. 320,000 of either ended the process.
		"more pipes than are read":    ":" + strings.Repeat("|:", 320000),
		"more commands than are read": ":" + strings.Repeat("&&:", 320000),
		"groups at the bound":         strings.Repeat("( ", 16000) + ":" + strings.Repeat(" )", 16000),
		"blocks at the bound":         strings.Repeat("{ ", 16000) + ":" + strings.Repeat("; }", 16000),
		"substitutions at the bound":  strings.Repeat("echo $( ", 12000) + ":" + strings.Repeat(" )", 12000),
		"pipes at the bound":          ":" + strings.Repeat("|:", 16000),
		"commands at the bound":       ":" + strings.Repeat("&&:", 8000),
		// A variable of many words, used many times.
		"a variable of many words": "v='" + strings.Repeat("a ", 2000) + "'; echo" + strings.Repeat(" $v", 2000),
		"lines in a pipe":          "echo" + strings.Repeat(" 'a b | sh'", 5000) + " | sh",
		// 🚨 Two more inputs that ended the process: signs in front of a
		// number, and an index in an index.
		"more signs than are read": "echo $((" + strings.Repeat("!", 650000) + "1))",
		"more signs of a sum":      "echo $((" + strings.Repeat("-+", 350000) + "1))",
		"pipes in blocks":          "echo x" + strings.Repeat(" | { cat", 4000) + strings.Repeat("; }", 4000),
		// 🚨 Four quoted lines, each inside 32 substitutions: 28 seconds for
		// 1.4 KiB when each level read the line inside it again.
		"quoted lines in evals":  quotedLines("eval $( ", " )", 32, 4),
		"quoted lines in shells": quotedLines("sh < <( ", " )", 60, 4),
		// 🚨 A command with variables that have no quotes is read one time
		// for each combination, and so is each shell inside it.
		"variables with no quotes in shells": variablesInShells(3, 2000, "", ""),
		"variables with no quotes in groups": variablesInShells(3, 2000, "( ", " )"),
		// 🚨 A substitution that the rule reads ahead of its command is
		// read one time. Two reads at each level are 2 to the power of 40.
		"substitutions in values":        strings.Repeat("X=$( ", 40) + "true" + strings.Repeat(" ) true", 40),
		"substitutions in redirects":     strings.Repeat("cat < <( ", 40) + "true" + strings.Repeat(" )", 40),
		"more tildes than are read":      "echo $((" + strings.Repeat("~", 650000) + "1))",
		"an index deeper than is read":   strings.Repeat("a[", 150000) + "0" + strings.Repeat("]", 150000) + "=1",
		"signs at the bound":             "echo $((" + strings.Repeat("!", 16000) + "1))",
		"an index at the bound":          strings.Repeat("a[", 16000) + "0" + strings.Repeat("]", 16000) + "=1",
		"a variable in many commands":    "v='" + strings.Repeat("a ", 2000) + "'; " + strings.Repeat("echo $v; ", 2000),
		"a printed variable, many times": "x=$(echo " + strings.Repeat("a ", 3000) + "); " + strings.Repeat("$x; ", 3000),
		"shells that feed shells":        strings.Repeat("sh < <(", 3000) + ":" + strings.Repeat(")", 3000),
		"loops in loops":                 strings.Repeat("while :;do ", 7000) + ":" + strings.Repeat(";done", 7000),
		"tags in groups":                 strings.Repeat("( git tag a; ", 8000) + ":" + strings.Repeat(" )", 8000),
		"one long word and many words":   strings.Repeat("a", 40000) + strings.Repeat(" b", 40000),
		"a long word behind a pipe":      "echo x | " + strings.Repeat("a", 40000) + strings.Repeat(" b", 40000),
	} {
		start := time.Now()
		(MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic))
		// Each line takes under a second with the race detector on. The two
		// defects that this test guards took 50 seconds and more.
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("%s: the rule took %s for a line of %d bytes", name, took, len(command))
		}
	}
}

// quotedLines returns a line of n groups around an echo of a quoted line of
// the same shape, to the given number of levels.
func quotedLines(open, shut string, n, levels int) string {
	line := "true"
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
	for range levels {
		line = strings.Repeat(open, n) + `echo "` + escape.Replace(line) + `"` + strings.Repeat(shut, n)
	}
	return line
}

// 🚨 A bound on the seconds of one read does not see a read that takes the
// square of its line: such a read of 36 KiB took four seconds. So this test
// reads each shape at two sizes, and the time may grow as the line grows and
// little faster. A read of each pipe at each level of a group grew 64 times
// for a line of 8 times the size.
func TestTheTimeOfAReadGrowsWithTheLine(t *testing.T) {
	read := func(command string) time.Duration {
		best := time.Hour
		for range 3 {
			start := time.Now()
			(MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic))
			best = min(best, time.Since(start))
		}
		return best
	}
	for _, tc := range []struct {
		name   string
		n, by  int
		shaped func(n int) string
	}{
		{"a pipe of shells", 1000, 8, func(n int) string { return "echo x" + strings.Repeat(" | sh", n) }},
		{"pipes in groups", 1000, 8, func(n int) string {
			return strings.Repeat("( ", n) + "echo x" + strings.Repeat(" | sh )", n)
		}},
		{"pipes in later groups", 1000, 8, func(n int) string {
			return "echo x" + strings.Repeat(" | ( cat", n) + strings.Repeat(" )", n)
		}},
		{"pipes in substitutions", 1000, 8, func(n int) string {
			return strings.Repeat("echo $( ", n) + "true" + strings.Repeat(" | sh )", n)
		}},
		{"lines in lines in a pipe", 8, 4, linesInLines},
		{"a variable used many times", 5000, 8, func(n int) string { return "v=a; echo" + strings.Repeat(" $v", n) }},
		{"evals of substitutions", 1000, 8, func(n int) string {
			return strings.Repeat("eval $( ", n) + "true" + strings.Repeat(" )", n)
		}},
		{"pipes in blocks", 1000, 8, func(n int) string {
			return "echo x" + strings.Repeat(" | { cat", n) + strings.Repeat("; }", n)
		}},
		{"a printed variable, many times", 500, 8, func(n int) string {
			return "x=$(echo " + strings.Repeat("a ", n) + "); " + strings.Repeat("$x; ", n)
		}},
		{"shells that feed shells", 500, 8, func(n int) string {
			return strings.Repeat("sh < <(", n) + ":" + strings.Repeat(")", n)
		}},
		{"loops in loops", 800, 8, func(n int) string {
			return strings.Repeat("while :;do ", n) + ":" + strings.Repeat(";done", n)
		}},
		{"one long word and many words", 5000, 8, func(n int) string {
			return strings.Repeat("a", n) + strings.Repeat(" b", n)
		}},
		{"a long word behind a pipe", 5000, 8, func(n int) string {
			return "echo x | " + strings.Repeat("a", n) + strings.Repeat(" b", n)
		}},
	} {
		small, large := read(tc.shaped(tc.n)), read(tc.shaped(tc.n*tc.by))
		t.Logf("%s: %s at %d, %s at %d", tc.name, small, tc.n, large, tc.n*tc.by)
		// A short read is all noise, so only a long one is judged. A read
		// that takes the square of its line grows by the square of tc.by,
		// which is twice the bound and more.
		if large > 300*time.Millisecond && large > small*time.Duration(tc.by)*4 {
			t.Errorf("%s: a line of %d times the size took %s, and the smaller one %s", tc.name, tc.by, large, small)
		}
	}
}

// linesInLines builds a line in a pipe, inside n groups that each pipe into
// a shell, with the same shape inside that line, four levels deep.
func linesInLines(n int) string {
	line := "true"
	for level := 0; level < 4; level++ {
		line = strings.Repeat("( ", n) + "echo " + strconv.Quote(line) + strings.Repeat(" | sh )", n)
	}
	return line
}

// What a second independent read found, of 669d51923: two defects, the
// routes that it proved, and the lines of daily work that the rule refused.
func TestWhatTheSecondReadFound(t *testing.T) {
	const (
		merges    = "merge"
		protected = "protected branch"
		unread    = "cannot read where its push goes"
	)
	up := with(topic, "upstream")
	rooted := topic
	rooted.Root = "/work/terva"
	onTrunk := RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}
	for _, tc := range []struct {
		command string
		facts   RepoFacts
		want    string
	}{
		// 🚨 An option at the end that takes a word. The rule read past the
		// words, and the process ended.
		{"git --foo -c", topic, ""},
		{"git --bare --git-dir", topic, ""},
		{"hub --x -C", topic, ""},

		// A new branch and its push on one line.
		{"git checkout -b fix/y && git add -A && git commit -m x && git push -u origin HEAD", topic, ""},
		{"git switch -c fix/y && git commit -m x && git push -u origin HEAD", topic, ""},
		{"git checkout sothr-main && git pull && git checkout -b fix/y && git push -u origin HEAD", topic, ""},
		{"git checkout -b fix/y && git push", topic, ""},
		{"git checkout -b fix/y && git push -u origin fix/y", up, ""},
		{"git checkout -B main && git push origin HEAD", topic, protected},
		// 🚨 A branch that starts at a remote branch follows it, and in the
		// upstream mode its push goes there. A probe with a real remote
		// moved trunk.
		{"git checkout -b fix/z origin/sothr-main && git push", up, unread},
		{"git checkout -b fix/z origin/sothr-main && git push -u origin fix/z", up, unread},
		{"git checkout -b fix/z origin/sothr-main && git push -u origin HEAD", up, ""},
		// The push stands in front of the new branch, or a later change
		// follows it, or a function hides the order.
		{"git push && git checkout -b fix/y", onTrunk, protected},
		{"git checkout -b fix/y && git checkout main && git push", topic, unread},
		{"p() { git push origin HEAD; }; git checkout main; git checkout -b fix/y; p", topic, unread},
		{"f() { git checkout main; }; git checkout -b fix/y; f; git push origin HEAD", topic, unread},
		{`git checkout -b "$name" && git push -u origin HEAD`, topic, unread},

		// A directory, and a group that ends its own change of it.
		{"cd /work/terva && git push -u origin feat/x", rooted, ""},
		{"cd /work/terva/ && git push", rooted, ""},
		{"git -C /work/terva push", rooted, ""},
		{"cd /work/other && git push -u origin feat/x", rooted, unread},
		{"cd /work/terva && git push -u origin feat/x", topic, unread},
		{"(cd client && npm run lint) && git push -u origin feat/x", topic, ""},
		{"echo $(cd client && pwd) && git push", topic, ""},
		{"(cd ../other && git push)", topic, unread},
		// 🚨 The branch and the configuration are on the disk, and a group
		// does not end a change of them.
		{"(git checkout main) && git push", topic, unread},
		{"(git config push.default upstream) && git push", topic, unread},
		{"echo $(git checkout main) && git push origin HEAD", topic, unread},
		{"(git checkout -b fix/y) && git push -u origin HEAD", topic, unread},
		{"(git checkout -b fix/y && git push -u origin HEAD)", topic, ""},
		{"(git tag v1 && git push origin v1)", up, ""},
		// The push is in no chain with the tag of the group.
		{"(git tag v1) && git push origin v1", up, unread},
		{"git -C /work/other push origin feat/x:feat/x", topic, ""},
		{"env -C ../other git push", topic, unread},
		{"env --chdir=../other git push origin HEAD", topic, unread},

		// git config: a value that the rule cannot read, and a read with no
		// option.
		{`git config user.email "$EMAIL" && git commit -m x && git push origin feat/x`, topic, ""},
		{`git config --global --add safe.directory "$PWD" && git push -u origin feat/x`, topic, ""},
		{"git config remote.origin.url && git push origin HEAD", topic, ""},
		{"git config --unset branch.feat/x.merge && git push", topic, unread},
		{"git -c color.ui=never push origin feat/x:feat/x", topic, ""},
		{"git -c color.ui=never push", topic, ""},
		{"git -c remote.origin.push=HEAD:main push origin feat/x:feat/x", topic, unread},
		{`git -c "$setting" push origin feat/x:feat/x`, topic, unread},

		// Other lines of daily work.
		{"git rebase --onto origin/sothr-main HEAD~3 && git push --force-with-lease", topic, ""},
		{`msg="just pr-merge refuses a draft"; git commit -m "$msg"`, topic, ""},
		{"just --show pr-merge", topic, ""},
		{"make -n pr-merge", topic, ""},
		{"rg -n mergePullRequest packages/", topic, ""},
		{"gh search prs merge rule", topic, ""},
		{"git push origin --delete old-topic", up, ""},
		{"git push -d origin old-topic", up, ""},
		{"git push origin --delete sothr-main", up, protected},
		{"git tag v0.142.0 && git push origin v0.142.0", up, ""},
		{"git tag -a v0.142.0 -m 'a release' && git push origin v0.142.0", up, ""},

		// A variable of the line, with its value in its place.
		{`b=main; git push origin "$b"`, topic, protected},
		{`b=feat/x; git push origin "$b"`, topic, ""},
		{`b=feat/x; git push origin "$b:$b"`, topic, ""},
		{`b=main; git push origin "HEAD:refs/heads/$b"`, topic, protected},
		{`b=$(pick); git push origin "$b:$b"`, topic, unread},
		{`GP="git push origin"; $GP HEAD:main`, topic, protected},
		{`p=(git push origin); "${p[@]}" HEAD:main`, topic, protected},
		{`for c in "git push origin HEAD:main" "git status"; do $c; done`, topic, protected},
		{`cmd="gh pr merge 12"; sh -c "$cmd"`, topic, merges},
		{`cmd=$(pick); $cmd`, topic, ""},
		// 🚨 A variable with two values on the line is one that the rule
		// cannot read: it does not know which one the push gets.
		{`b=main; test -n "$x" && b=feat/x; git push origin "$b"`, topic, unread},
		{`b=feat/x; (b=main); git push origin "$b"`, topic, unread},
		{`b=feat/x; b=$(pick); b=feat/x; git push origin "$b"`, topic, unread},
		{`b=feat/x; b=feat/x; git push origin "$b"`, topic, ""},

		// 🚨 The routes that the read proved.
		{"gh pr checkout 12 && git push", topic, unread},
		{`bash -c "git push origin $'HEAD:ma\x69n'"`, topic, unread},
		{"bash <<'EOF'\ngit push origin $'HEAD:ma\\x69n'\nEOF", topic, unread},
		// The words that the substitution prints are read in front of the
		// words of the command, and git is among them.
		{`"$(command -v git)" push origin HEAD:main`, topic, "protected branch"},
		{`"$GIT" push origin HEAD:main`, topic, unread},
		{`"$(command -v just)" pr-merge 12`, topic, merges},
		{"git rebase -x 'git push origin HEAD:main' HEAD~1", topic, protected},
		{"git submodule foreach 'gh pr merge 12'", topic, merges},
		{"git push --recurse-submodules no origin", onTrunk, unread},
		{"git push --no-such-option origin feat/x:feat/x", topic, unread},
		{"git push -fu origin feat/x", topic, ""},
		{"gh pr -R o/r merge 12\n(", topic, merges},
		{"glab mr accept 12\n(", topic, merges},
		{"tea pr m 12\n(", topic, merges},
		{"echo 'gh pr merge 12' | bash -c 'sh'", topic, merges},
		{"echo 'gh pr merge 12' | { tee /dev/null | sh; }", topic, merges},
		{"echo 'gh pr merge 12' | xargs -I{} sh -c '{}'", topic, merges},
		{"echo 'gh pr merge 12' | busybox sh", topic, merges},
		{`echo "pr merge 12" | xargs gh`, topic, merges},
		{"echo 12 | xargs gh pr view", topic, ""},
		{`eval "$(echo 'gh pr merge 12')"`, topic, merges},
		{"bash <(echo 'gh pr merge 12')", topic, merges},
		{"source <(echo 'gh pr merge 12')", topic, merges},
		{`eval "$(ssh-agent -s)"`, topic, ""},
		{"printf '[push]\n\tdefault = upstream\n' >> .git/config; git push", topic, unread},
		{"gh alias set land 'pr merge' && gh land 12", topic, merges},
		{"gh alias set co 'pr checkout'", topic, ""},
		{"git push origin HEAD:refs/for/main%submit", topic, merges},
		{"git push origin HEAD:refs/for/main", topic, ""},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, tc.command), factsOf(tc.facts))
		if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
			t.Errorf("%q gave %q, want %q", tc.command, why, tc.want)
		}
	}
}

// What a third read found, of d33b15eee: a second input that ended the
// process, a route through an ordinary line, and more.
func TestWhatTheThirdReadFound(t *testing.T) {
	const (
		merges    = "merge"
		protected = "protected branch"
		unread    = "cannot read where its push goes"
		refused   = "push"
	)
	up := with(topic, "upstream")
	rooted := topic
	rooted.Root = "/work/terva"
	inherits := up
	inherits.AutoSetupMerge = "inherit"
	onTrunk := RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}
	for _, tc := range []struct {
		command string
		facts   RepoFacts
		want    string
	}{
		// 🚨 The branch exists, so the create fails, and no && stops the
		// push. A probe with a real remote moved trunk with each line.
		{"git checkout -b fix/y; git add -A; git commit -m x; git push -u origin HEAD", onTrunk, protected},
		{"git checkout -b fix/y\ngit push -u origin HEAD", onTrunk, protected},
		{"git switch -c fix/y 2>/dev/null; git push -u origin HEAD", onTrunk, protected},
		{"git checkout -b fix/y; git push", onTrunk, protected},
		{`[ -n "$NEW" ] && git checkout -b fix/z; git push origin HEAD`, onTrunk, protected},
		{"git checkout -b fix/y || true; git push origin HEAD", onTrunk, protected},
		{"! git checkout -b fix/y && git push origin HEAD", onTrunk, protected},
		{"git checkout -b fix/y & git push origin HEAD", onTrunk, protected},
		// With && the push runs only on the new branch.
		{"git checkout -b fix/y && git add -A && git commit -m x && git push -u origin HEAD", onTrunk, ""},
		{"git fetch && git checkout -b fix/y && git push", onTrunk, ""},
		// On a branch of its own the line passes either way.
		{"git checkout -b fix/y; git add -A; git commit -m x; git push -u origin HEAD", topic, ""},
		// A create that the shell does not run as it stands is no create.
		{"trap 'git checkout -b fix/z' EXIT && git push origin HEAD", onTrunk, protected},
		{"echo git checkout -b fix/z && git push origin HEAD", onTrunk, protected},
		{"GIT_DIR=../other/.git git checkout -b fix/z && git push origin HEAD", onTrunk, protected},
		{"git config alias.nb 'checkout -b fix/z' && git push origin HEAD", onTrunk, protected},
		{"sh -c 'git checkout -b fix/z' && git push origin HEAD", onTrunk, protected},
		// A new branch follows its start under this setting.
		{"git checkout -b fix/y && git push -u origin fix/y", inherits, unread},

		// A branch that the line checks out by its name.
		{"git checkout feat/x && git pull && git push", topic, ""},
		{"git fetch origin && git rebase origin/sothr-main feat/x && git push --force-with-lease", topic, ""},
		{"git rebase origin/sothr-main main && git push", topic, unread},
		{"git checkout -b fix/y && git checkout feat/x && git push", topic, ""},
		{"git checkout -q feat/y && git push", topic, unread},
		{`git checkout "$(git branch --show-current)" && git push`, topic, ""},

		// just takes its options in front of the recipe.
		{"just pr-merge 12 -n", topic, merges},
		{"just pr-merge 12 --dry-run", topic, merges},
		{"just pr-merge --list", topic, merges},
		{"just -n pr-merge 12", topic, ""},

		// A word with no variable that the rule still cannot read.
		{`gh pr $'m\x65rge' 12`, topic, merges},
		{`bash -c "gh pr $'m\x65rge' 12"`, topic, merges},

		// 🚨 A variable that the line changes where the rule does not see
		// the value.
		{`r=feat/x:feat/x; read r <<< HEAD:main; git push origin "$r"`, topic, unread},
		{`r=feat/x:feat/x; printf -v r HEAD:main; git push origin "$r"`, topic, unread},
		{`r=feat/x:feat/x; eval 'r=HEAD:main'; git push origin "$r"`, topic, unread},
		{`r=feat/x:feat/x; set_r() { r=HEAD:main; }; set_r; git push origin "$r"`, topic, unread},
		{`b=feat/x:feat/x; for i in 1 2; do git push origin "$b"; b=HEAD:main; done`, topic, unread},
		{`b=feat/y; unset b; git push origin $b`, onTrunk, unread},
		{`b=feat/y true; git push origin $b`, onTrunk, unread},
		{`IFS=,; r=feat/x:feat/x,HEAD:main; git push origin $r`, topic, unread},
		{`declare -n q=r; r=feat/x:feat/x; q=HEAD:main; git push origin "$r"`, topic, unread},
		// A variable with two values keeps them both.
		{`ok=1; act=view; [ -n "$ok" ] && act=merge; gh pr $act 12`, topic, merges},
		{`ok=1; r=pr-status; [ -n "$ok" ] && r=pr-merge; just $r 12`, topic, merges},
		// A line in a variable that a shell gets.
		{`cmd="git push origin HEAD:main"; echo "$cmd" | bash`, topic, refused},
		{`cmd="git push origin HEAD:main"; bash <<< "$cmd"`, topic, protected},
		{`x=$(echo 'gh pr merge 12'); $x`, topic, merges},
		{`x=$(echo 'gh pr merge 12'); eval "$x"`, topic, merges},

		// git's own words.
		{"git tag -l feat/x; git push origin feat/x", up, protected},
		{"git tag feat/x; git push origin feat/x", up, protected},
		{"git -c alias.p=push p origin HEAD:main", topic, unread},
		{`git config --comment a.b "$(echo push.default)" upstream && git push`, topic, unread},
		{"printf '[push]\n\tdefault = upstream\n' | tee -a .git/config >/dev/null; git push", topic, unread},
		{"git \\\npush origin HEAD:main\n(", topic, refused},
		{"gh pr -R o/r \\\nmerge 12\n(", topic, merges},
		{"git push origin HEAD:refs/for/main%topic=x,submit", topic, merges},
		{"git push -o submit origin HEAD:refs/for/main", topic, merges},
		{"git push origin HEAD:refs/for/main%topic=submit-fix", topic, ""},

		// What a shell is fed.
		{"sh < <(echo 'gh pr merge 12')", topic, merges},
		{"$(echo 'gh pr merge 12')", topic, merges},
		{`echo 'gh pr merge 12' | { read -r l; eval "$l"; }`, topic, merges},
		{"echo 'gh pr merge 12' | script -qc sh /dev/null", topic, merges},
		{"echo 'gh pr merge 12' | script -qc 'sh -s' /dev/null", topic, merges},
		{"echo 'gh pr merge 12' | flock /tmp/l sh", topic, merges},
		{"git log --oneline | grep -c sh", topic, ""},
		{"ls | xargs -n1 basename | sort | grep bash", topic, ""},

		// The directory.
		{"cd -P /work/terva/link/.. && git push", rooted, unread},
		{"cd /work/terva/packages/.. && git push", rooted, unread},
		{`cd "$(git rev-parse --show-toplevel)" && git push -u origin feat/x`, topic, ""},

		// Lines of daily work that the rule refused.
		{"git push --help", up, ""},
		{"man git push", up, ""},
		{"git push --dry-run origin feat/x", up, ""},
		{"grep -rn git push docs/", up, ""},
		{`BRANCH=feat/x; git push origin "$BRANCH:$BRANCH"`, topic, ""},
		{`b=feat/x; git push -u origin "HEAD:refs/heads/$b"`, topic, ""},
		{"git push -u origin feat/x --force-with-leas", topic, ""},
		{"cat > big.go <<'EOF'\n" + strings.Repeat("(", 10001) + "\nEOF", topic, ""},
		{"n=12; gh pr view $n", topic, ""},
		{`msg="fix: a thing"; git commit -m "$msg"`, topic, ""},
		{`branch=$(git branch --show-current); git push -u origin "$branch"`, topic, ""},
		{`branch=$(git branch --show-current); git push -u origin "$branch"`, up, protected},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, tc.command), factsOf(tc.facts))
		if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
			t.Errorf("%q gave %q, want %q", tc.command, why, tc.want)
		}
	}
}

// 🚨 An object can spell the key of a command in many ways, and each one
// was one more read of every word: 21 seconds for 227 KiB.
// What the author found in a read of the fourth round, each with a probe
// against a real repository or the program's own manual.
func TestWhatAReadOfTheFourthRoundFound(t *testing.T) {
	const (
		merges    = "merge"
		protected = "protected branch"
		unread    = "cannot read where its push goes"
	)
	up := with(topic, "upstream")
	onTrunk := RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}
	for _, tc := range []struct {
		command string
		facts   RepoFacts
		want    string
	}{
		// 🚨 -n is the value of -o here, and git pushed main.
		{"git push -o -n origin HEAD:main", topic, protected},
		{"git push -o --help origin HEAD:main", topic, protected},
		{"git push --repo -n origin HEAD:main", topic, protected},
		{"git push --push-option --dry-run origin HEAD:main", topic, protected},
		{"git push $o --dry-run origin HEAD:main", topic, unread},
		{"git push --recurse-submodules --dry-run origin HEAD:main", topic, unread},
		{"git push --dry-run origin HEAD:main", topic, ""},
		{"git push origin HEAD:main --dry-run", topic, ""},
		{"git push -n", up, ""},

		// Commands of git that change the branch at work.
		{"git rebase --root main && git push", topic, unread},
		{"git rebase --root feat/x && git push", topic, ""},
		{"git rebase --root && git push --force-with-lease", topic, ""},
		{"git stash branch release && git push origin HEAD", topic, unread},
		{"git stash && git pull --rebase && git stash pop && git push", topic, ""},
		{"git bisect reset main && git push", topic, unread},

		// 🚨 A tag that the line did not make is the name of a branch.
		{"git tag other no-such-commit; git push origin other", up, unread},
		{"git tag -m other v1; git push origin other", up, unread},
		{"git tag -m other v1 && git push origin other", up, unread},
		{"echo git tag other && git push origin other", up, unread},
		{"git -C ../other tag other && git push origin other", up, unread},
		{"git tag v1 && git push origin v1", up, ""},
		{"git tag -a -m 'a release' v1 && git push origin v1", up, ""},
		{"git tag v1; git push origin v1", up, unread},

		// A name that the line gives to another program.
		{`grep() { "$@"; }; grep git push origin HEAD:main`, topic, protected},
		{"shopt -s expand_aliases\nalias man=env\nman git push origin HEAD:main", topic, protected},
		{"man -P 'git push origin HEAD:main' ls", topic, protected},
		{"git -c core.pager='git push origin HEAD:main' log", topic, protected},
		{"GIT_PAGER='git push origin HEAD:main' git log", topic, protected},
		{"export GIT_PAGER='git push origin HEAD:main'; git log", topic, protected},
		{"GIT_SSH_COMMAND='ssh -i key' git fetch", topic, ""},
		{"git -c user.name='A Person' commit -m x", topic, ""},

		// A variable that changes where the rule did not look.
		{`b=feat/x:feat/x; while :; do git push origin "$b"; read b; done <<< HEAD:main`, topic, unread},
		{`b=feat/x:feat/x; trap 'b=HEAD:main' DEBUG; git push origin "$b"`, topic, unread},
		{`b=; : ${b:=HEAD:main}; git push origin $b`, topic, unread},
		{`b=; : ${b=HEAD:main}; git push origin $b`, topic, unread},
		{`export b; git push origin $b`, topic, unread},
		{`b=feat/x; export b; git push origin "$b:$b"`, topic, ""},

		// An option that prints a recipe is the first word.
		{"just --shell-arg -n pr-merge 12", topic, merges},
		{"make -o -n pr-merge", topic, merges},
		{"make pr-merge -n", topic, merges},
		{"just --show pr-merge", topic, ""},
		{"make -n pr-merge", topic, ""},

		// 🚨 The shell reads a word with ?, * or [ as a pattern for the
		// names of files.
		{"/usr/bin/gi[t] push origin HEAD:main", topic, unread},
		{"/usr/bin/gi? push origin HEAD:main", topic, unread},
		{"git pus[h] origin HEAD:main", topic, unread},
		{"touch HEAD:main; git push origin HEAD:mai?", topic, unread},
		{"git push origin HEAD:[m]ain", topic, unread},
		{"touch merge; gh pr merg? 12", topic, merges},
		{"gh pr m[e]rge 12", topic, merges},
		{"gh pr * 12", topic, merges},
		{"tea pr [m] 12", topic, merges},
		{"gh pr list --json number --jq .[].number", topic, ""},
		{"gh pr view 12 --jq .files[0].path", topic, ""},
		{`[ -n "$x" ] && git push origin feat/x:feat/x`, topic, ""},
		{`[ "$a" = push ] && git status`, topic, ""},
		{"git add packages/*.go && git commit -m x && git push", topic, ""},

		// A chain of && is the one thing that makes a new branch sure.
		{"sh -c 'git checkout -b fix/y; git push -u origin HEAD'", onTrunk, protected},
		{"git checkout -b fix/y; sh -c 'git push -u origin HEAD'", onTrunk, protected},
		{"git checkout -b fix/y || git push origin HEAD", onTrunk, protected},
		{"git checkout --detach feat/x && git push", topic, unread},
		{`git rebase origin/sothr-main "$b" && git push`, topic, unread},
		{"git tag feat/x && git push origin feat/x", up, protected},
		{"git tag -l other && git push origin other", up, unread},
		{"git --git-dir=../other/.git push --dry-run", topic, ""},
		{"git checkout --detach && git push", topic, unread},
		{"git --git-dir=../other/.git tag other && git push origin other", up, unread},
		// 🚨 A function runs where the line calls it, and that is behind
		// the new branch here.
		{"f() { git checkout main; }; git checkout -b fix/y && f && git push origin HEAD", topic, unread},
		{`r=feat/x:feat/x; g() { git push origin "$r"; }; f() { r=HEAD:main; }; f; g`, topic, unread},
		{"busybox env GIT_DIR=../other/.git git push", topic, unread},
		{"echo 'gh pr merge 12' | bash -c 'cat | sh'", topic, merges},

		// A variable inside a word.
		{`b=feat/x; eval 'b=main'; git push origin "HEAD:$b"`, topic, unread},
		{`b=feat/x; [ -n "$x" ] && b=main; git push origin "HEAD:$b"`, topic, unread},
		{`b='x main:main'; git push origin HEAD:$b`, topic, unread},

		// 🚨 A command that is a variable, which the line fills where the
		// rule does not see the value.
		{`read c <<< 'gh pr merge 12'; $c`, topic, "the variable c"},
		{`printf -v c 'gh pr merge %s' 12; $c`, topic, "the variable c"},
		{`while read -r c; do $c; done <<< 'gh pr merge 12'`, topic, "the variable c"},
		// The shell of the line keeps what an eval sets, and the rule reads
		// the same assignments.
		{`c="git status"; eval 'c="git push origin HEAD:main"'; $c`, topic, protected},
		{`eval 'c="gh pr merge 12"'; $c`, topic, merges},
		// A script on the disk is one that the rule cannot read.
		{`c="git status"; source ./env.sh; $c`, topic, ""},
		{`cmd="go test ./..."; $cmd`, topic, ""},
		{`PY=$(command -v python3); $PY x.py`, topic, ""},
		{`source ./env.sh && $PYTHON x.py`, topic, ""},

		// More words than the rule reads in one command.
		{"v='" + strings.Repeat("a ", 300) + "'; git push origin feat/x:feat/x" + strings.Repeat(" $v", 300), topic, "this long"},
		{"v='git push origin" + strings.Repeat(" a", 20000) + "'; $v; $v; $v; $v", topic, "this long"},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, tc.command), factsOf(tc.facts))
		if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
			t.Errorf("%q gave %q, want %q", tc.command, why, tc.want)
		}
	}
}

// What a fourth read found, of 0fd9b51c5: one route through an ordinary
// line, and more that take intent. The author found one more of the first
// kind on the way.
func TestWhatTheFourthReadFound(t *testing.T) {
	const (
		merges    = "merge"
		protected = "protected branch"
		unread    = "cannot read where its push goes"
		refused   = "push"
	)
	up := with(topic, "upstream")
	onTrunk := RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}
	// own is a branch that follows a branch of its own name.
	own := with(RepoFacts{Branch: "feat/x", Defaults: []string{"sothr-main"}, Upstream: "feat/x"}, "upstream")
	for _, tc := range []struct {
		command string
		facts   RepoFacts
		want    string
	}{
		// 🚨 -B takes a branch that exists, and the branch keeps what it
		// follows. A probe moved main with the first line.
		{"git checkout -B feat/x && git push", up, unread},
		{"git switch -C feat/x && git push", up, unread},
		{"git checkout -B fix/y && git push origin fix/y", up, unread},
		{"git checkout -B feat/x && git push", topic, ""},
		{"git checkout -b fix/y && git push -u origin fix/y", up, ""},

		// 🚨 A push is read against the change of the branch in front of
		// it, and not against the last one of the line.
		{"git checkout -B main && git push origin HEAD && git checkout -b other", topic, protected},
		{"git checkout -b fix/y && git push origin HEAD && git checkout -B main", topic, ""},
		// A change behind the push is no change for it.
		{"git push origin HEAD && git checkout main", topic, ""},
		{"git push -u origin HEAD; git checkout -", topic, ""},
		{"git checkout main && git push origin HEAD && git checkout feat/x", topic, unread},
		// The last checkout in the chain of the push is the branch.
		{"git checkout main && git pull && git checkout feat/x && git rebase main && git push --force-with-lease", topic, ""},
		{"git checkout main; git checkout feat/x; git push", topic, unread},
		{"git checkout feat/x && git checkout main && git push", topic, unread},
		{"echo git checkout feat/x && git push", onTrunk, unread},
		// A pipe behind the push keeps the chain, and a pipe behind the
		// new branch hides whether it was made.
		{"git checkout -b fix/y && git add -A && git commit -m x && git push -u origin HEAD 2>&1 | tail -3", onTrunk, ""},
		{"git checkout -b fix/y | cat && git push origin HEAD", onTrunk, protected},
		{"git tag other no-such-commit | cat && git push origin other", up, unread},

		// A runner that runs nothing, and a ! that turns an end around.
		{"env --version git checkout -b fix/z && git push origin HEAD", onTrunk, protected},
		{"env --version git tag other && git push origin other", up, unread},
		{"true && ! git checkout -b fix..z && git push origin HEAD", onTrunk, protected},
		{"true && ! git tag other no-such && git push origin other", up, unread},
		{"git tag other -l && git push origin other", up, unread},
		{"git tag other --list && git push origin other", up, unread},
		{"git tag -am 'a release' v1.2.3 && git push origin v1.2.3", up, ""},
		{"git tag -af v1.2.3 && git push origin v1.2.3", up, ""},

		// The branch at work in a variable is the branch at that place.
		{`b=$(git branch --show-current); git checkout -b fix/q && git push origin "$b"`, onTrunk, unread},
		{`b=$(git branch --show-current); git checkout main && git push origin "$b:$b"`, topic, unread},
		{`b=$(git branch --show-current); git push -u origin "$b"`, topic, ""},
		{`b=$(git branch --show-current); git push -u origin "$b"`, up, protected},

		// 🚨 A variable with no quotes may be no word at all.
		{"(b=feat/y:feat/y); git push origin $b", onTrunk, protected},
		{"false && b=feat/y:feat/y; git push origin $b", onTrunk, protected},
		{"b=feat/y:feat/y | true; git push origin $b", onTrunk, protected},
		{"b=feat/y; u=unset; $u b; git push origin $b", onTrunk, unread},
		{`b=feat/y:feat/y; git push origin "$b"`, onTrunk, ""},
		{"b=feat/y:feat/y; git push origin $b", topic, ""},
		{"b=fix/y; git checkout -b $b && git push -u origin $b", onTrunk, ""},

		// Variables that the shell fills or changes by itself.
		{`declare -l r=FEAT/X:MAIN; git push origin "$r"`, topic, unread},
		{`declare -x r=feat/x:feat/x; git push origin "$r"`, topic, ""},
		{`printf -v IFS ,; r=feat/x:feat/x,HEAD:main; git push origin $r`, topic, unread},
		{`_=feat/x:feat/x; : HEAD:main; git push origin "$_"`, topic, unread},
		{`REPLY=feat/x:feat/x; read <<< HEAD:main; git push origin "$REPLY"`, topic, unread},
		{`b=(HEAD:main feat/x:feat/x); git push origin "$b"`, topic, protected},
		{`b=(feat/x:feat/x HEAD:main); git push origin "${b[@]}"`, topic, protected},
		{`r=feat/x:feat/x; c=read; $c r <<< HEAD:main; git push origin "$r"`, topic, unread},
		{`sh -c 'r=feat/x:feat/x; r=HEAD:main :; git push origin "$r"'`, topic, unread},
		{`_=view; : merge; gh pr $_ 12`, topic, merges},
		{`: merge; gh pr $_ 12`, topic, merges},
		{`: 'gh pr merge 12'; $_`, topic, "the variable _"},
		{`a=(merge view); gh pr "$a" 12`, topic, merges},
		{`a=(gh pr merge 12); "${a[@]}"`, topic, merges},
		{`read -r n < prs.txt; gh pr view "$n"`, topic, ""},
		{`read -r act < act.txt; gh pr "$act" 12`, topic, merges},

		// A push that sends nothing, and a word that takes that back.
		{"git push -n --no-dry-run origin HEAD:main", topic, unread},
		{"echo '--no-dry-run origin HEAD:main' | xargs git push -n", topic, unread},
		{`git push origin HEAD:{main,"x"}`, topic, unread},
		{`gh pr {merge,"--squash"} 12`, topic, merges},
		{"git -c help.autocorrect=immediate psuh origin HEAD:main", topic, unread},
		{"git config help.autocorrect immediate && git psuh origin HEAD:main", topic, unread},
		{`git remote add tag "$url" && git push --repo=x tag feat/x:main`, topic, protected},
		{"git push --repo=origin tag v1", topic, ""},

		// What a shell is fed.
		{"echo 'gh pr merge 12' | bash /dev/stdin", topic, merges},
		{"echo 'gh pr merge 12' | source /dev/stdin", topic, merges},
		{"echo 'gh pr merge 12' | bash <(cat)", topic, merges},
		{"echo 'gh pr merge 12' | bash -c 'source /dev/stdin'", topic, merges},
		{"source /dev/stdin <<< 'gh pr merge 12'", topic, merges},
		{"sh <<EOF\n$(echo 'gh pr merge 12')\nEOF", topic, merges},
		{"$(echo gh pr) merge 12", topic, merges},
		{"echo merge | xargs -I{} gh pr {} 12", topic, merges},
		{"echo merge | xargs -I % gh pr % 12", topic, merges},
		{"echo 12 | xargs -I{} gh pr view {}", topic, ""},

		// Patterns for the names of files, and quotes in a line that does
		// not parse.
		{"touch pr; gh p? merge 12", topic, merges},
		{"touch push; /usr/bin/gi[t] pus[h] origin HEAD:main", topic, unread},
		{"gi''t pu''sh origin HEAD:main; cat <<EOF", topic, refused},
		{"g''h p''r mer''ge 12; cat <<EOF", topic, merges},
		{"source scripts/pr.sh merge 12", topic, merges},
		{"flock /tmp/l ./scripts/pr.sh merge 12", topic, merges},
		{"cat scripts/pr.sh", topic, ""},
		// A recipe behind a program that the rule cannot read, and words
		// that are a line only together.
		{`"$JUST" pr-merge 12`, topic, merges},
		{"echo cd /elsewhere '&&' git push | sh", topic, unread},
		{"cp scripts/pr.sh merge.sh", topic, ""},

		// More of the branch in front of a push.
		{"git checkout main && (git push origin HEAD)", topic, unread},
		{"git checkout feat/x && git push origin HEAD && (git checkout main)", topic, ""},
		{"git checkout main; git checkout -b fix/y; git push origin HEAD", topic, unread},
		{"git checkout -B feat/x origin/main; git checkout feat/x; git push", own, unread},
		{"git checkout main && echo git checkout feat/x && git push", topic, unread},
		{"git checkout main && git checkout feat/x | cat && git push", topic, unread},
		{"git checkout main && git --git-dir=../o/.git checkout feat/x && git push", topic, unread},
		// More of the variables.
		{`_=feat/x; : main; git push origin "HEAD:$_"`, topic, unread},
		{`c="gh pr merge 12"; "${c[@]}"`, topic, merges},
		{`b=feat/x; eval "$X"; git push origin "HEAD:$b"`, topic, unread},
		{`b=feat/x; source ./env.sh; git push origin "HEAD:$b"`, topic, unread},
		{"v='" + strings.Repeat("a ", 300) + "'; " + strings.Repeat("echo $v; ", 300), topic, "this long"},
		{"echo merge | xargs --replace=@ gh pr @ 12", topic, merges},
		{"echo merge | xargs -i gh pr {} 12", topic, merges},

		// Lines of daily work that the rule refused.
		{`source .env; cmd="go test ./..."; $cmd`, topic, ""},
		{`eval "$(mise activate bash)"; $EDITOR README.md`, topic, ""},
		{`eval "$(direnv export bash)"; "$PYTHON" -m pytest`, topic, ""},
		{`trap 'rm -f /tmp/x' EXIT; b=feat/x; git push origin "$b:$b"`, topic, ""},
		{`trap 'b=main' DEBUG; b=feat/x; git push origin "HEAD:$b"`, topic, unread},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, tc.command), factsOf(tc.facts))
		if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
			t.Errorf("%q gave %q, want %q", tc.command, why, tc.want)
		}
	}
	// A group deeper than the rule reads.
	deep := strings.Repeat("( ", 65) + "git push origin HEAD:main" + strings.Repeat(" )", 65)
	if why := (MergeRule{}).Match("bash", bashCall(t, deep), factsOf(topic)); !strings.Contains(why, "this long or this deep") {
		t.Errorf("65 groups gave %q", why)
	}
	for tool, args := range map[string]string{
		"mcp_github_push_files":           `{"branch":"feat/x","targetBranch":"main"}`,
		"mcp_forgejo_push_files":          `{"branch":"feat/x","dest":"main"}`,
		"mcp_gitlab_push_files":           `{"branch":"feat/x","target_branch":"main"}`,
		"mcp_exec_run":                    `{"command":["gh"],"args":["pr","merge","12"]}`,
		"mcp_gerrit_submit_change":        `{"id":1}`,
		"mcp_github_enqueue_pull_request": `{"number":12}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), factsOf(topic)); why == "" {
			t.Errorf("the rule let %s %s pass", tool, args)
		}
	}
	for tool, args := range map[string]string{
		"mcp_github_submit_pending_pull_request_review": `{"number":12}`,
		"mcp_github_push_files":                         `{"branch":"feat/x","targetBranch":"feat/x"}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), factsOf(topic)); why != "" {
			t.Errorf("the rule refused %s %s: %s", tool, args, why)
		}
	}
}

// What a fifth read found, of e71432a8f: no route through an ordinary
// line and no input that ends the process, one slow shape, and routes that
// take intent.
func TestWhatTheFifthReadFound(t *testing.T) {
	const (
		merges    = "merge"
		protected = "protected branch"
		unread    = "cannot read where its push goes"
	)
	onTrunk := RepoFacts{Branch: "sothr-main", Defaults: []string{"sothr-main"}, Upstream: "sothr-main"}
	// own is a branch that follows a branch of its own name.
	own := with(RepoFacts{Branch: "feat/x", Defaults: []string{"sothr-main"}, Upstream: "feat/x"}, "upstream")
	for _, tc := range []struct {
		command string
		facts   RepoFacts
		want    string
	}{
		// 🚨 A new upstream in front of a checkout of the same branch.
		{"git branch -u origin/main && git checkout feat/x && git push", own, unread},
		{"git checkout -B feat/x origin/main && git commit --allow-empty -m x && git checkout feat/x && git push", own, unread},
		{"git checkout -b fix/y && git checkout feat/x && git push", own, ""},

		// 🚨 The shell runs a substitution before the command that holds
		// it, and a loop runs its commands again.
		{"X=$(git checkout -q -B main) git push origin HEAD", topic, unread},
		{`git push origin HEAD > "$(git checkout -q -B main; echo /dev/null)"`, topic, unread},
		{`git push origin "$(git checkout -q main; echo HEAD)"`, topic, unread},
		{"for i in 1 2; do git push origin HEAD; git checkout -q -B main; done", topic, protected},
		{"for i in 1 2; do sh -c 'git push origin HEAD'; git checkout -q -B main; done", topic, protected},
		{"for i in 1 2; do (git push origin HEAD); git checkout -q -B main; done", topic, protected},
		{"for i in 1 2; do sh -c '(git push origin HEAD)'; git checkout -q -B main; done", topic, protected},
		{"while :; do git push origin HEAD; git checkout main; done", topic, unread},
		{"for f in a b; do echo $f; done; git push origin HEAD && git checkout main", topic, ""},

		// 🚨 A word from a variable with no quotes may be no word.
		{"(x=view); gh pr $x merge 12", topic, merges},
		{"(x=stash); git $x push origin HEAD:main", topic, protected},
		{"(o=origin); git push --repo $o -n origin HEAD:main", topic, protected},
		{"(t=tag); git push origin $t main", topic, protected},
		{"x=12; gh pr view $x", topic, ""},
		{"b=fix/y; git checkout -b $b && git push -u origin $b", onTrunk, ""},
		{"m=fix; git commit -m $m && git push", topic, ""},

		// Variables.
		{`b=([1]=feat/x:feat/x [0]=HEAD:main); git push origin "$b"`, topic, unread},
		{`b=feat/x:feat/x; printf -v "$(echo b)" HEAD:main; git push origin "$b"`, topic, unread},
		{`b=feat/x:feat/x; declare "$(echo b)=HEAD:main"; git push origin "$b"`, topic, unread},
		{`b=feat/x; printf '%s\n' "$y"; git push origin "$b:$b"`, topic, ""},
		{`trap 'for b in HEAD:main; do :; done' DEBUG; b=feat/x:feat/x; git push origin "$b"`, topic, unread},
		{`c="git status"; eval "$(echo c=)'gh pr merge 12'"; $c`, topic, "the variable c"},
		{`eval "$(direnv export bash)"; cmd="go test ./..."; $cmd`, topic, ""},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, tc.command), factsOf(tc.facts))
		if (tc.want == "") != (why == "") || !strings.Contains(why, tc.want) {
			t.Errorf("%q gave %q, want %q", tc.command, why, tc.want)
		}
	}
}

func TestAnObjectOfManyCommandsIsRefused(t *testing.T) {
	var keys []string
	for mask := range 1 << len("executable") {
		key := []byte("executable")
		for i := range key {
			if mask&(1<<i) != 0 {
				key[i] -= 'a' - 'A'
			}
		}
		keys = append(keys, strconv.Quote(string(key))+`:"gh"`)
	}
	args := "{" + strings.Join(keys, ",") + `,"args":[` + strings.Repeat(`"x",`, 50000) + `"y"]}`
	start := time.Now()
	why := (MergeRule{}).Match("mcp_exec_run", json.RawMessage(args), nil)
	if took := time.Since(start); why == "" || took > 20*time.Second {
		t.Errorf("the rule gave %q after %s for an object of %d bytes", why, took, len(args))
	}
	// 🚨 Arguments that Go's reader refuses get no pass.
	deep := strings.Repeat(`{"a":`, 20000) + `{"command":"gh pr merge 12"}` + strings.Repeat("}", 20000)
	if why := (MergeRule{}).Match("mcp_shell_run", json.RawMessage(deep), nil); why == "" {
		t.Error("the rule let arguments pass that it could not read")
	}
	// A text past the depth that the rule reads is still read.
	nine := `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"command":"gh pr merge 12"}}}}}}}}}`
	if why := (MergeRule{}).Match("mcp_shell_run", json.RawMessage(nine), nil); !strings.Contains(why, "merge") {
		t.Errorf("a command nine objects deep gave %q", why)
	}
}

func TestAToolThatPushesNamesItsBranch(t *testing.T) {
	facts := factsOf(topic)
	for tool, args := range map[string]string{
		"mcp_git_push":           `{"remote":"origin","refspec":"HEAD:main"}`,
		"mcp_git_git_push":       `{"repo_path":"/work"}`,
		"mcp_github_push_files":  `{"owner":"o","repo":"r","branch":"main","files":[]}`,
		"mcp_forgejo_push":       `{"branch":"sothr-main"}`,
		"mcp_git_push_branch":    `{"options":{"ref":"refs/heads/trunk"}}`,
		"mcp_gitlab_push_commit": `{"branch":"HEAD"}`,
		// 🚨 A tool that runs git gives git a refspec. A name with one side
		// goes to the upstream of that branch in the upstream mode, and the
		// rule does not know the mode of the tool's repository.
		"mcp_git_push_one":     `{"repo_path":"/work","branch":"feat/x"}`,
		"mcp_git_push_all":     `{"branch":"feat/x:feat/x","all":true}`,
		"mcp_git_push_to":      `{"branch":"feat/x","remote_branch":"main"}`,
		"mcp_github_push_file": `{"branch":" main"}`,
		"mcp_vcs_push":         `{"branch":"main"}`,
		"mcp_any_push":         `{"refspec":"HEAD:main"}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), facts); why == "" {
			t.Errorf("the rule let %s %s pass", tool, args)
		}
	}
	for tool, args := range map[string]string{
		"mcp_git_push":           `{"remote":"origin","refspec":"feat/x:feat/x"}`,
		"mcp_github_push_files":  `{"owner":"o","repo":"r","branch":"feat/x","files":[]}`,
		"mcp_pushover_push":      `{"message":"the run ended"}`,
		"mcp_notify_push_notice": `{"text":"merge waits for a person"}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), facts); why != "" {
			t.Errorf("the rule refused %s %s: %s", tool, args, why)
		}
	}
}

func TestAToolsNameAndWordsAreRead(t *testing.T) {
	for tool, args := range map[string]string{
		"mcp_bitbucket_mergePullRequest":  `{}`,
		"mcp_forgejo_merge":               `{"index":12}`,
		"mcp_github_enable_auto_merge":    `{"n":12}`,
		"mcp_gitlab_accept_merge_request": `{"iid":12}`,
		"mcp_github_add_to_merge_queue":   `{"n":12}`,
		"enable_auto_merge":               `{"n":12}`,
		"add_to_merge_queue":              `{"n":12}`,
		// 🚨 The program under one name, and its words under another.
		"mcp_proc_run":   `{"command":"gh","args":["pr","merge","12"]}`,
		"mcp_proc_spawn": `{"cmd":"git","argv":["push","origin","HEAD:main"]}`,
		"mcp_proc_exec":  `{"command":"bash","args":["-c","gh pr merge 12"]}`,
		"mcp_proc_shell": `{"command":"bash","args":["-c","./scripts/pr.sh merge 12"]}`,
		"mcp_shell_many": `{"commands":["go test ./...","gh pr merge 12"]}`,
		// 🚨 Words that would open a quote in a line. The tool starts the
		// program with the words, and no shell reads them.
		"mcp_exec_list":   `{"command":["env","A='","gh","pr","merge","12","B='"]}`,
		"mcp_exec_prog":   `{"program":"gh","args":["pr","merge","12"]}`,
		"mcp_exec_runner": `{"command":"runner","args":["gh pr merge 12"]}`,
		"mcp_exec_argv":   `{"argv":["gh","pr","merge","12"]}`,
		"mcp_exec_string": `{"command":"git","args":"push origin HEAD:main"}`,
		// 🚨 Arguments past the depth that the rule reads.
		"mcp_exec_deep":         `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"command":"gh pr merge 12"}}}}}}}}}}`,
		"accept_mr":             `{"iid":12}`,
		"complete_pull_request": `{"id":12}`,
		"PRMerge":               `{"n":12}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), nil); why == "" {
			t.Errorf("the rule let %s %s pass", tool, args)
		}
	}
	// 🔑 GitLab's merge request is a thing and no action. A chain may open
	// one and read one.
	for tool, args := range map[string]string{
		"mcp_gitlab_create_merge_request": `{"title":"a fix"}`,
		"mcp_gitlab_get_merge_request":    `{"iid":12}`,
		"mcp_gitlab_list_merge_requests":  `{}`,
		"mcp_git_merge_base":              `{"a":"x","b":"y"}`,
		"mcp_proc_run":                    `{"command":"gh","args":["pr","view","12"]}`,
		"mcp_proc_exec":                   `{"command":"go","args":["test","./..."]}`,
		// A name that says get, list or search reads.
		"mcp_forgejo_get_pull_request_merge_status": `{"index":12}`,
		"mcp_github_get_merge_queue":                `{}`,
		"mcp_github_search_code":                    `{"q":"func mergeBranch"}`,
		// accept and complete merge only a pull request.
		"mcp_github_accept_repository_invitation": `{"id":1}`,
		"mcp_gitlab_complete_todo":                `{"id":1}`,
		"mcp_deep_values":                         `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":"a text"}}}}}}}}}`,
	} {
		if why := (MergeRule{}).Match(tool, json.RawMessage(args), nil); why != "" {
			t.Errorf("the rule refused %s %s: %s", tool, args, why)
		}
	}
}

// The forge's security review of 1f16006f9 (review 3226).
func TestWhatTheSecurityReviewFound(t *testing.T) {
	// A path that curl and the forge clean before they read it.
	api := "curl -X POST https://forge/api/v1/repos/o/r/pulls/12"
	for command, want := range map[string]string{
		api + "/./merge":         "merge endpoint",
		api + "//merge":          "merge endpoint",
		api + "/.//./merge":      "merge endpoint",
		api + "/x/../merge":      "merge endpoint",
		api + "/x/y/../../merge": "merge endpoint",
		api + "/%6derge":         "merge endpoint",
		api + "/%6D%65rge":       "merge endpoint",
		api + "/%2e/merge":       "merge endpoint",
		// The words with their quotes removed.
		api + "/'.'/merge": "merge endpoint",
		// A path in a here document, which is no word of the command.
		"curl -X POST -K - <<EOF\nurl = https://forge/api/v1/repos/o/r/pulls/12/./merge\nEOF": "merge endpoint",
		// A line in a line, with the path in a variable.
		"p=12/./merge; sh -c \"curl -X POST https://forge/api/v1/repos/o/r/pulls/$p\"": "merge endpoint",
		// A line in a line that does not parse.
		"p=12/./merge; sh -c \"curl -X POST https://forge/api/v1/repos/o/r/pulls/$p '\"": "merge endpoint",
		api + "/files":               "",
		api + "/../../merge-base":    "",
		api + "/x/../comments":       "",
		"date +%d%m%y && git status": "",
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, command), factsOf(topic))
		if (want == "") != (why == "") || !strings.Contains(why, want) {
			t.Errorf("%q gave %q, want %q", command, why, want)
		}
	}
	if why := (MergeRule{}).Match("mcp_http_request", json.RawMessage(`{"url":"https://forge/api/v1/repos/o/r/pulls/12/./merge"}`), nil); !strings.Contains(why, "merge endpoint") {
		t.Errorf("a tool with a path that is not clean gave %q", why)
	}

	// 🚨 Arguments of the bash tool that the rule cannot read get no pass.
	for args, want := range map[string]string{
		`{"command":["git","push","origin","HEAD:main"]}`: "cannot read the arguments",
		`{"command":`: "cannot read the arguments",
		``:            "",
		` `:           "",
	} {
		why := (MergeRule{}).Match("bash", json.RawMessage(args), factsOf(topic))
		if (want == "") != (why == "") || !strings.Contains(why, want) {
			t.Errorf("bash with the arguments %q gave %q, want %q", args, why, want)
		}
	}

	// A tool of an extension with many pushes asks for the facts one time.
	asked := 0
	facts := func() RepoFacts { asked++; return topic }
	many := `{"steps":[` + strings.Repeat(`{"command":"git push origin feat/x:feat/x"},`, 40) + `{"command":"git push origin feat/y:feat/y"}]}`
	if why := (MergeRule{}).Match("mcp_shell_run", json.RawMessage(many), facts); why != "" {
		t.Errorf("a tool with many pushes of a topic branch gave %q", why)
	}
	if asked != 1 {
		t.Errorf("the rule read the repository %d times for one call, want 1", asked)
	}
}

// variablesInShells returns shells inside shells. Each one sets four
// variables and uses them with no quotes, around n commands that do so too.
// open and shut stand around each shell.
func variablesInShells(levels, n int, open, shut string) string {
	sets := "a=1; b=2; c=3; d=4; "
	line := sets + strings.Repeat("git status $a $b $c $d; ", n)
	for range levels {
		line = sets + open + "sh -c '" + strings.ReplaceAll(line, "'", `'\''`) + "' $a $b $c $d" + shut
	}
	return line
}

// The forge's code review of 79cbfc369 (review 3227).
func TestWhatTheReviewOfThatFixFound(t *testing.T) {
	unread := "cannot read where its push goes"
	for _, c := range []struct {
		command string
		facts   RepoFacts
		want    string
	}{
		// 🚨 One variable holds nothing, and the next one holds its value.
		{"(o=-n); r=origin; git push $o $r HEAD:main", topic, "protected branch main"},
		{"false && o=--dry-run; r=origin; git push $o $r HEAD:main", topic, "protected branch main"},
		{"sh -c '(o=-n); r=origin; git push $o $r HEAD:main'", topic, "protected branch main"},
		{"(c=status); p=push; git $c $p origin HEAD:main", topic, "protected branch main"},
		{"(a=view); m=merge; n=12; gh pr $a $m $n", topic, "merge"},
		// More variables than the rule reads in turn.
		{"(o=-n); r=origin; b=HEAD:main; x=-q; y=-v; git push $o $x $y $r $b", topic, unread},
		{"a=1; b=2; c=3; d=4; e=5; echo $a $b $c $d $e", topic, ""},
		// The words of one variable stay or go together.
		{"o='-u origin'; git push $o feat/x:feat/x", topic, ""},
		{"r=origin; b=feat/x:feat/x; git push $r $b", topic, ""},
		{"r=origin; f=-q; git push $f $r HEAD:refs/heads/feat/x", topic, ""},
	} {
		why := (MergeRule{}).Match("bash", bashCall(t, c.command), factsOf(c.facts))
		if (c.want == "") != (why == "") || !strings.Contains(why, c.want) {
			t.Errorf("%q gave %q, want %q", c.command, why, c.want)
		}
	}

	// A merge command of the user configuration, with a word between.
	lands := MergeRule{Commands: []*regexp.Regexp{regexp.MustCompile(`^just land \d+$`)}}
	if why := lands.Match("bash", bashCall(t, "(a=x); n=12; just $a land $n"), factsOf(topic)); !strings.Contains(why, "user configuration") {
		t.Errorf("a merge command behind a variable that holds nothing gave %q", why)
	}

	// 🚨 The bytes of those reads have a bound. The 15 reads of one command
	// fit in it at any length, and the 225 reads of a shell in a shell do
	// not.
	script := strings.Repeat("git status; ", 3000)
	sets := "a=1; b=2; c=3; d=4; "
	inner := sets + `sh -c "` + script + `" $a $b $c $d`
	why := (MergeRule{}).Match("bash", bashCall(t, sets+"sh -c '"+inner+"' $a $b $c $d"), factsOf(topic))
	if !strings.Contains(why, "this long") {
		t.Errorf("a long script that the rule reads 225 times gave %q", why)
	}
	why = (MergeRule{}).Match("bash", bashCall(t, sets+"sh -c '"+script+"' $a $b $c $d"), factsOf(topic))
	if why != "" {
		t.Errorf("a long script that the rule reads 15 times gave %q", why)
	}
}
