package workspace

// The merge rule of a chain that no person began (decision 0029 rules 14
// and 15).
//
// Such a chain has an attended agent's authority minus the merge. It may
// push its own branch and open a pull request. It never merges, and it never
// moves a protected branch.
//
// 🔑 The rule is a rung of the session's gate, ahead of the hooks and the
// policy, and it is no rule of the policy. A policy is built one time for a
// session. A member's turn is unattended for one chain and attended for the
// next, so the rung reads the turn at each call.
//
// 🚨 The rule reads a command's text. It tells a model at each route why a
// merge is not its to make, and a shell can still get past it. The guard
// that holds is the entity's forge account, which has no right to merge to
// trunk (permissions.MergeRule, docs/permissions.md).

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/permissions"
)

// repoFactsWait bounds each git command that reads the repository for the
// merge rule.
const repoFactsWait = 3 * time.Second

// mergeGuard refuses a merge action of a turn with no person at its root. It
// is a build.CallGuard.
func (s *wsSession) mergeGuard(ctx context.Context, tool string, args json.RawMessage) string {
	// The rule gives a first-party tool but bash no word. This return only
	// saves the read of the turn at each such call.
	if tool != "bash" && permissions.IsBuiltin(tool) {
		return ""
	}
	if _, lone := s.unattended(); !lone {
		return ""
	}
	return mergeDenial(ctx, s.cwd, tool, args)
}

// mergeDenial is the merge rule for one call of work that no person began.
// It returns why the call is refused, or an empty text. The session's guard
// and the guard of a swarm child (UnattendedChildRungs) both end here, so
// the two refuse the same calls with the same words.
func mergeDenial(ctx context.Context, dir, tool string, args json.RawMessage) string {
	// Each first-party tool but bash runs no command and reaches no forge.
	// TestEachFirstPartyToolHasAPlaceInTheMergeRule holds the list, so a
	// new one does not get this pass in silence.
	if tool != "bash" && permissions.IsBuiltin(tool) {
		return ""
	}
	// Read live, as the wait for a person is. A person who adds a pattern
	// binds the chains that are at work.
	pol := config.TalkootRoutines()
	rule := permissions.MergeRule{Commands: pol.MergeCommands, Protected: pol.ProtectedBranches}
	// 🔑 dir is the directory in which the bash tool starts each call. The
	// tool takes no directory, and a cd in one call does not reach the next
	// one. So these are the facts of the repository that a push runs in,
	// for a line that does not change the directory itself.
	why := rule.Match(tool, args, func() permissions.RepoFacts { return repoFacts(ctx, dir) })
	if why == "" {
		return ""
	}
	return "tool call denied by the unattended merge rule: " + why + ". " +
		"No person began this chain. Such a chain may push its own branch and open a pull request, and it never merges. " +
		"Do not try another route. Leave the merge to a person, and say in your last message what waits for them."
}

// repoConfigArgs reads the settings that place a push with no branch:
// push.default, the upstream of each branch, what a new branch follows, and
// each remote's push refspec and mirror mark.
// A test replaces it with a command that fails.
var repoConfigArgs = []string{"config", "--get-regexp", `^(push\.default|branch\.autosetupmerge|branch\..*\.merge|remote\..*\.(push|mirror))$`}

// repoFacts reads what the merge rule needs to know about the repository in
// dir.
//
// 🚨 A repository that git cannot read is unknown, and it is not a
// repository with no branch. The rule refuses a push that it cannot place.
func repoFacts(ctx context.Context, dir string) permissions.RepoFacts {
	unknown := permissions.RepoFacts{Unknown: true}
	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, repoFactsWait)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		// 🚨 A command that the wait ended is no answer, whatever it
		// printed, and whatever status the kill left.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return strings.TrimSpace(string(out)), err
	}
	var f permissions.RepoFacts
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return unknown
	}
	// A detached HEAD prints HEAD, and a push from it names no branch.
	if branch != "HEAD" {
		f.Branch = branch
	}
	// The top level, so that the rule knows cd to it from cd out of it. A
	// bare repository has none. The rule then reads each cd as a way out,
	// which is the careful answer, so a failure here is no unknown
	// repository.
	if root, err := git("rev-parse", "--show-toplevel"); err == nil {
		f.Root = filepath.ToSlash(root)
	}
	refs, err := git("for-each-ref", "--format=%(refname) %(symref)", "refs/remotes")
	if err != nil {
		return unknown
	}
	for _, line := range strings.Split(refs, "\n") {
		// refs/remotes/origin/HEAD refs/remotes/origin/trunk
		name, target, _ := strings.Cut(line, " ")
		if remote, ok := strings.CutSuffix(name, "/HEAD"); ok {
			if b, ok := strings.CutPrefix(target, remote+"/"); ok {
				f.Defaults = append(f.Defaults, b)
			}
		}
	}
	// 🚨 git config ends with status 1 when no key matches, and that one
	// status is an empty answer. Any other failure is no answer: a read
	// that failed must not stand for a repository with the default mode,
	// in which a push with no branch is the safe one.
	conf, err := git(repoConfigArgs...)
	var status *exec.ExitError
	if err != nil && (!errors.As(err, &status) || status.ExitCode() != 1) {
		return unknown
	}
	for _, line := range strings.Split(conf, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch {
		case key == "push.default":
			f.PushDefault = strings.TrimSpace(val)
		case key == "branch.autosetupmerge":
			// git reads true and false in any case. The rule reads every
			// other word as a branch that follows its start.
			f.AutoSetupMerge = strings.ToLower(strings.TrimSpace(val))
		case strings.HasSuffix(key, ".mirror"):
			// 🚨 git push to a mirror moves every ref, with no --mirror in
			// the command. Only a value that git reads as false is safe.
			switch strings.ToLower(strings.TrimSpace(val)) {
			case "false", "no", "off", "0":
			default:
				f.PushRefspecs = true
			}
		case strings.HasPrefix(key, "remote."):
			f.PushRefspecs = true
		case f.Branch != "" && key == "branch."+f.Branch+".merge":
			f.Upstream = strings.TrimPrefix(val, "refs/heads/")
		}
	}
	return f
}
