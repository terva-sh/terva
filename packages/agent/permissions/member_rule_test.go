package permissions

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/testsupport"
)

// A Talkoot member works tickets only through the ticket tools, which write
// under its actor, record its claims, and check its authorship. Its bash rule
// refuses the git ticket CLI, first and in yolo, however the command is
// spelled, and refuses nothing else.
func TestAMemberCannotUseTheTicketCLIThroughBash(t *testing.T) {
	withTempHome(t)
	cfg := config.Config{Permissions: []config.PermissionRuleConfig{{Tool: "bash", Decision: "allow"}}}
	in := Inputs{Mode: mode.Print, CWD: testsupport.TempDir(t), Approval: "yolo", TalkootMember: true}
	pol, _, err := policyFromConfig(in, cfg)
	if err != nil || pol == nil {
		t.Fatalf("policy: %v, %v", pol, err)
	}
	verdict := func(cmd string) permission.PolicyVerdict {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		v, _ := pol.Evaluate("bash", args)
		return v
	}
	for _, cmd := range []string{
		"git ticket status TKT-1 done",
		"git ticket status TKT-1 archived --reason gone",
		"git ticket status TKT-1 --reason x done",
		"git ticket status TKT-1 Done",
		`git ticket status TKT-1 "done"`,
		"git ticket --json status TKT-1 done",
		"git ticket archive TKT-1",
		"git ticket claim TKT-1",
		"git ticket 'claim' TKT-1",
		"git 'ticket' claim TKT-1",
		"git t'ick'et claim TKT-1",
		`git \ticket claim TKT-1`,
		"git  ticket claim TKT-1",
		"git\tticket claim TKT-1",
		"GIT Ticket claim TKT-1",
		"git -C /repo ticket status TKT-1 done",
		"git -c user.name=x ticket claim TKT-1",
		"git --no-pager ticket claim TKT-1",
		"git --git-dir /repo/.git ticket claim TKT-1",
		"git --work-tree=/repo ticket claim TKT-1",
		"/usr/bin/git ticket claim TKT-1",
		"git-ticket status TKT-1 done",
		"~/.local/bin/git-ticket claim TKT-1",
		"git ticket list",
		"git ticket",
		"cd /repo && git ticket status TKT-1 done",
		"echo hi; git ticket claim TKT-1",
		"g'i't ticket claim TKT-1",
		`"g"it ticket claim TKT-1`,
		`g\it ticket claim TKT-1`,
		"git $'ticket' claim TKT-1",
		"git ticket>/dev/null claim TKT-1",
		`git -C "/my repo" ticket claim TKT-1`,
		`git -c 'a=b c' ticket claim TKT-1`,
		"(git ticket claim TKT-1)",
		"x=$(git ticket claim TKT-1)",
		"sh -c 'git ticket claim TKT-1'",
		`bash -lc "git ticket claim TKT-1"`,
		"sudo git ticket claim TKT-1",
		"env A=1 git ticket claim TKT-1",
		"A=1 B='x y' git ticket claim TKT-1",
		"if true; then git ticket claim TKT-1; fi",
		`find . -exec git ticket claim TKT-1 \;`,
		"echo TKT-1 | xargs git ticket claim",
		`eval "git ticket claim TKT-1"`,
		"timeout 5 git ticket claim TKT-1",
		`git --git-dir="/my repo/.git" ticket claim TKT-1`,
		"git --git-dir='/my repo/.git' --no-pager ticket claim TKT-1",
		"sudo -u root git ticket claim TKT-1",
		"nice -n 10 git ticket status TKT-1 done",
		"echo TKT-1 | xargs -n 1 git ticket claim",
		"timeout -s KILL 5 git ticket claim TKT-1",
		"env -u FOO git ticket claim TKT-1",
		"stdbuf -oL git ticket claim TKT-1",
		"setsid git ticket claim TKT-1",
		"doas git ticket claim TKT-1",
		`find . -execdir git ticket claim TKT-1 \;`,
		"/bin/sh -c 'git ticket claim TKT-1'",
	} {
		if v := verdict(cmd); v != permission.VerdictDeny {
			t.Errorf("%q: verdict %v, want deny", cmd, v)
		}
	}
	for _, cmd := range []string{
		"git status",
		"git log --grep ticket",
		`git commit -m "the ticket claim is done"`,
		"git --no-pager log -- .tickets/",
		"cat .tickets/tickets/TKT-1.md",
		"legit ticket claim",
		"digit ticket claim",
		"git ticketing",
		"git log -- src/git-ticket.go",
		"ls .git/ticket",
		"echo 'git ticket claim TKT-1'",
		`git commit -m 'document git ticket claim'`,
		`grep -rn "git ticket" docs`,
		`grep -c 'git ticket' docs`,
		`git log -c -S 'git ticket'`,
		"printf '%s\\n' git ticket claim",
	} {
		if v := verdict(cmd); v != permission.VerdictAllow {
			t.Errorf("%q: verdict %v, want allow", cmd, v)
		}
	}

	// A session that is not a member keeps its rules as they were.
	in.TalkootMember = false
	pol, _, _ = policyFromConfig(in, cfg)
	args, _ := json.Marshal(map[string]string{"command": "git ticket status TKT-1 done"})
	if v, _ := pol.Evaluate("bash", args); v != permission.VerdictAllow {
		t.Errorf("a session with no seat: verdict %v, want allow", v)
	}
}
