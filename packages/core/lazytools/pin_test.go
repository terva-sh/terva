package lazytools

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// A request names the groups its own specs leave out, which are the pinned
// ones. With continuation off, a group activated mid-segment is live in the
// component and still hidden from the segment's requests, so the note keeps
// naming it until the next Prompt pins it.
func TestTheNoteFollowsThePinNotTheLiveSet(t *testing.T) {
	client := &reqCaptureClient{toolThen: "activate"}
	reg := core.Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &groupActivatorTool{group: "mail"},
		"mail_send": extTool("mail_send", "mail"),
	}
	a, v := newAgent(client, reg, core.AllowAll)
	v.SetContinuation(false)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.ephemeral) != 2 {
		t.Fatalf("got %d requests, want the tool call and the answer", len(client.ephemeral))
	}
	if specNames(client.tools[1])["mail_send"] {
		t.Fatal("precondition: with continuation off the activation re-pinned the segment")
	}
	if !strings.Contains(client.ephemeral[1], "mail_send") {
		t.Errorf("the second request hides mail_send but its note does not name it: %q", client.ephemeral[1])
	}
}

// An essential tool was advertised at the pin, so activating a group that
// holds only essential tools makes nothing newly live, and the activation gate
// has nothing to announce.
func TestActivatingAnEssentialOnlyGroupContinuesNothing(t *testing.T) {
	var v *Visibility
	client := &reqCaptureClient{}
	client.onCall = func(n int) {
		if n == 1 {
			v.Activate("x") // off the tool path, as a host would
		}
	}
	reg := core.Registry{
		"read":  &flagTool{name: "read"},
		"x_ess": essentialExtTool("x_ess", "x"),
	}
	var a *core.Agent
	a, v = newAgent(client, reg, core.AllowAll)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if n := len(client.ephemeral); n != 1 {
		t.Errorf("the Prompt ran %d requests, want 1: x_ess was already live, so there is nothing to continue for", n)
	}
	if !specNames(client.tools[0])["x_ess"] {
		t.Error("precondition: the essential tool was not advertised before its group was active")
	}
}

// A restore replaces the session's groups and keeps the configured ones.
func TestARestoreKeepsTheConfiguredGroups(t *testing.T) {
	client := &reqCaptureClient{}
	reg := core.Registry{
		"read":      &flagTool{name: "read"},
		"mail_send": extTool("mail_send", "mail"),
		"gh_pr":     extTool("gh_pr", "mcp:github"),
		"cal_add":   extTool("cal_add", "cal"),
	}
	a, v := newAgent(client, reg, core.AllowAll, "mail")
	v.Activate("cal")
	v.RestoreActiveGroups([]string{"mcp:github"})
	if got := strings.Join(v.Active(), ","); got != "mail,mcp:github" {
		t.Errorf("active after the restore = %s, want mail,mcp:github: the configured group stays and the outgoing session's goes", got)
	}
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	adv := specNames(client.tools[0])
	if !adv["mail_send"] || !adv["gh_pr"] || adv["cal_add"] {
		t.Errorf("advertised %v, want mail_send and gh_pr and not cal_add", adv)
	}
}
