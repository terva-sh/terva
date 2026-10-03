package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

const jevEither = "    workspace: either\n"

// movableSetup starts the crew talkoot with jev movable, on fake leases that
// hand out real directories, and returns jev's live session after its first
// turn. jev's lease directory is a git repository with a branch fix/x.
func movableSetup(t *testing.T) (*Workspace, *fakeLeases, string, *wsSession) {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	fl := &fakeLeases{root: testsupport.TempDir(t)}
	w.talkootLeases = fl
	dir := leaseDir(fl, "jev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitT(t, dir, "branch", "fix/x")
	if _, err := w.talkootCreate(t.Context(), "crew", nativeWorktreeCrew(cwd, jevEither)); err != nil {
		t.Fatalf("a movable member was refused: %v", err)
	}
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	s := memberLiveSession(t, w, "jev")
	awaitTurnEnd(t, "jev")
	return w, fl, cwd, s
}

// gitT runs git for the test itself, never for the code under test.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func moveTool(t *testing.T, s *wsSession) *tools.TalkootWorkspaceTool {
	t.Helper()
	tool, ok := s.agent.LookupTool("talkoot_workspace")
	if !ok {
		t.Fatal("a movable member has no talkoot_workspace")
	}
	return tool.(*tools.TalkootWorkspaceTool)
}

func move(t *testing.T, s *wsSession, action string) (string, error) {
	t.Helper()
	res, err := moveTool(t, s).Execute(t.Context(), json.RawMessage(`{"action":"`+action+`"}`), nil)
	if err != nil {
		return "", err
	}
	return toolText(res), nil
}

func toolCWD(t *testing.T, s *wsSession, name string) string {
	t.Helper()
	tool, ok := s.agent.LookupTool(name)
	if !ok {
		return ""
	}
	switch v := tool.(type) {
	case *tools.ReadTool:
		return v.CWD
	case *tools.WriteTool:
		return v.CWD
	case *tools.BashTool:
		return v.CWD
	}
	t.Fatalf("%s is a %T", name, tool)
	return ""
}

func released(fl *fakeLeases, member string) bool {
	_, rel := fl.snapshot()
	return slices.Contains(rel, member)
}

// A movable member starts in the home checkout in plan, whatever its roster
// posture, so it cannot write where the one-writer rule assumes it does not.
func TestAMovableMemberStartsReadOnlyInTheHomeCheckout(t *testing.T) {
	w, _, cwd, s := movableSetup(t)
	if s.cwd != cwd || toolCWD(t, s, "read") != cwd {
		t.Fatalf("jev starts in %q, read in %q, want the home checkout %q", s.cwd, toolCWD(t, s, "read"), cwd)
	}
	if got := w.talkootPostureOf(s.id); got != "plan" {
		t.Fatalf("jev outside its worktree runs in %q, want plan", got)
	}
	for _, name := range []string{"write", "edit", "bash"} {
		if _, ok := s.agent.LookupTool(name); ok {
			t.Errorf("jev outside its worktree has %s", name)
		}
	}
	moveTool(t, s)
}

// Enter moves the member into its worktree, with write and bash there in its
// roster posture, and keeps its conversation.
func TestEnterMovesAMemberIntoItsWorktree(t *testing.T) {
	w, fl, _, s := movableSetup(t)
	before := len(s.agent.Messages())
	text, err := move(t, s, "enter")
	if err != nil {
		t.Fatal(err)
	}
	dir := leaseDir(fl, "jev")
	if s.cwd != dir || toolCWD(t, s, "read") != dir || toolCWD(t, s, "write") != dir || toolCWD(t, s, "bash") != dir {
		t.Fatalf("after enter jev is in %q, read %q, write %q, bash %q, want %q",
			s.cwd, toolCWD(t, s, "read"), toolCWD(t, s, "write"), toolCWD(t, s, "bash"), dir)
	}
	if got := w.talkootPostureOf(s.id); got != "auto-edit" {
		t.Fatalf("jev in its worktree runs in %q, want its roster posture", got)
	}
	if !strings.Contains(text, "git switch") {
		t.Errorf("enter must tell jev to switch branches itself: %q", text)
	}
	if len(s.agent.Messages()) != before {
		t.Fatal("the move lost the conversation")
	}
}

// Leave moves the member home, read-only, and keeps its lease, so a later
// enter returns to the same worktree.
//
// 🚨 A release runs the worktree engine's Reclaim, which runs git status from
// the daemon in the worktree. A member must not trigger that, so leave never
// releases. (Review of #1572, round 5.)
//
// ⚠️ The release half proves a negative. A release is a goroutine that calls
// release at once, so the bounded wait is ample for it to run.
func TestLeaveMovesAMemberHomeAndKeepsTheLease(t *testing.T) {
	w, fl, cwd, s := movableSetup(t)
	if _, err := move(t, s, "enter"); err != nil {
		t.Fatal(err)
	}
	if _, err := move(t, s, "leave"); err != nil {
		t.Fatal(err)
	}
	if s.cwd != cwd || toolCWD(t, s, "read") != cwd || w.talkootPostureOf(s.id) != "plan" {
		t.Fatalf("after leave jev is in %q in %q", s.cwd, w.talkootPostureOf(s.id))
	}
	if _, ok := s.agent.LookupTool("write"); ok {
		t.Error("jev kept write in the home checkout")
	}
	// A second leave from outside changes nothing.
	if _, err := move(t, s, "leave"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if released(fl, "jev") {
		t.Fatal("leave gave the lease back, which runs daemon git in the worktree")
	}
	if _, err := move(t, s, "enter"); err != nil {
		t.Fatal(err)
	}
	if s.cwd != leaseDir(fl, "jev") {
		t.Fatalf("a second enter moved jev to %q, want its worktree", s.cwd)
	}
}

// 🚨 A move runs no git. The daemon runs outside the member's sandbox and
// gate, and the member can write its worktree's hooks, config, and
// info/attributes. Here each of those names a command, the worktree is dirty
// so a status would read file content, and a full enter, status, and leave
// must run none of them. (Review of #1572: GIT_ATTR_SOURCE does not cover
// info/attributes, and no git option covers every file.)
func TestAMoveRunsNoGit(t *testing.T) {
	_, fl, _, s := movableSetup(t)
	dir := leaseDir(fl, "jev")
	marker := filepath.Join(testsupport.TempDir(t), "ran")
	trap := "touch " + marker + "; cat"
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "a.txt")
	gitT(t, dir, "commit", "-q", "-m", "a")
	gitT(t, dir, "config", "filter.x.clean", trap)
	gitT(t, dir, "config", "filter.x.smudge", trap)
	gitT(t, dir, "config", "core.fsmonitor", trap)
	for name, body := range map[string]string{
		filepath.Join(".git", "info", "attributes"):     "* filter=x\n",
		filepath.Join(".git", "hooks", "post-checkout"): "#!/bin/sh\n" + trap + "\n",
		"a.txt": "b",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"enter", "status", "leave", "status"} {
		if _, err := move(t, s, action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a move ran a command that the member's worktree names")
	}
}

// A failed enter leaves the member outside and read-only.
func TestAFailedEnterLeavesTheMemberOutside(t *testing.T) {
	w, fl, cwd, s := movableSetup(t)
	fl.mu.Lock()
	fl.err = errors.New("no worktree for you")
	fl.mu.Unlock()
	if _, err := move(t, s, "enter"); err == nil {
		t.Fatal("enter succeeded with no lease")
	}
	if s.cwd != cwd || w.talkootPostureOf(s.id) != "plan" {
		t.Fatalf("after a failed enter jev is in %q in %q", s.cwd, w.talkootPostureOf(s.id))
	}
	if _, ok := s.agent.LookupTool("write"); ok {
		t.Error("a failed enter gave jev write in the home checkout")
	}
}

// A member inside its worktree starts the next run in the home checkout, in
// plan, and its lease from the last run goes back.
func TestARestartStartsAMovableMemberOutside(t *testing.T) {
	w, fl, cwd, s := movableSetup(t)
	if _, err := move(t, s, "enter"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootLeases = fl
	w2.LoadTalkoots()
	s2 := memberLiveSession(t, w2, "jev")
	if s2.cwd != cwd || w2.talkootPostureOf(s2.id) != "plan" {
		t.Fatalf("after a restart jev is in %q in %q, want the home checkout in plan", s2.cwd, w2.talkootPostureOf(s2.id))
	}
	if _, ok := s2.agent.LookupTool("write"); ok {
		t.Error("after a restart jev has write in the home checkout")
	}
	waitTalkoot(t, "jev's lease to go back", func() bool { return released(fl, "jev") })
}

// A roster update and a move are serialized, so a move cannot land between
// the postures an update computes and the postures it applies.
func TestARosterUpdateWaitsForAMove(t *testing.T) {
	w, _, cwd, _ := movableSetup(t)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	run.moveMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := w.talkootUpdate(t.Context(), "crew", "sothr", nativeWorktreeCrew(cwd, jevEither+"    posture: ask\n"))
		done <- err
	}()
	select {
	case err := <-done:
		run.moveMu.Unlock()
		t.Fatalf("the update did not wait for the move: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	run.moveMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A member that is not movable has no talkoot_workspace.
func TestAWorktreeMemberCannotMove(t *testing.T) {
	w, _, _ := nativeWorktreeSetup(t)
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	s := memberLiveSession(t, w, "jev")
	awaitTurnEnd(t, "jev")
	if _, ok := s.agent.LookupTool("talkoot_workspace"); ok {
		t.Fatal("a workspace: worktree member has talkoot_workspace")
	}
}
