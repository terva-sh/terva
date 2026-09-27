package workspace

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// toolsCrew is crewText with jev's own lines after its role.
func toolsCrew(home, jev string) string {
	return "---\nname: crew\nhome: " + home + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n" + jev + "---\n"
}

// jevTools starts the talkoot, gives jev a turn, and returns jev's session
// and the names of the tools jev's first request offered the model. The
// request is what the member received, whatever a later rebuild does.
func jevTools(t *testing.T, jev string) (*Workspace, *wsSession, []string) {
	t.Helper()
	cwd := talkootHome(t)
	var (
		mu      sync.Mutex
		offered []string
		asked   bool
	)
	provider := func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		if !asked {
			asked = true
			for _, tool := range req.Tools {
				offered = append(offered, tool.Function.Name)
			}
			slices.Sort(offered)
		}
		mu.Unlock()
		okProvider(rw, r)
	}
	w := openTalkootWorkspaceWith(t, cwd, provider)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: toolsCrew(cwd, jev)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"jev"}, Body: "Read the schema."}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "jev's turn", func() bool {
		m := memberView(t, w, "crew", "jev")
		return m.Status.Turns == 1 && !m.Status.Working
	})
	s := w.existing(memberView(t, w, "crew", "jev").Session)
	if s == nil {
		t.Fatal("jev has no session")
	}
	mu.Lock()
	defer mu.Unlock()
	if !asked || len(offered) == 0 {
		t.Fatal("jev's turn made no request that offered tools")
	}
	return w, s, offered
}

func toolNames(s *wsSession) []string {
	var out []string
	for name := range s.agent.ToolsSnapshot() {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// 🚨 A member with a tools list sees only those tools and its seat. Every
// other tool, a host tool injected after Resolve included, leaves the
// registry, so the gate refuses a call to it as unknown.
func TestAMemberReceivesOnlyItsTools(t *testing.T) {
	_, _, got := jevTools(t, "    tools: [read]\n")
	if !slices.Contains(got, "read") || !slices.Contains(got, "talkoot_send") {
		t.Fatalf("jev lost a tool its list or its seat names: %v", got)
	}
	for _, name := range got {
		if name != "read" && !slices.Contains(talkoot.SeatTools, name) {
			t.Errorf("jev has %s, which its list does not name", name)
		}
	}
}

// A member without a list keeps the posture's full set.
func TestAMemberWithoutAListKeepsThePostureSet(t *testing.T) {
	_, _, got := jevTools(t, "")
	for _, want := range []string{"read", "grep", "glob", "talkoot_send"} {
		if !slices.Contains(got, want) {
			t.Errorf("jev lost %s with no list: %v", want, got)
		}
	}
}

// 🚨 A list never widens. jev plans, and a list that names write does not
// give it write.
func TestAToolsListNeverWidensThePosture(t *testing.T) {
	_, _, got := jevTools(t, "    tools: [read, write, bash]\n")
	if !slices.Contains(got, "read") {
		t.Fatalf("jev lost read: %v", got)
	}
	for _, name := range []string{"write", "bash"} {
		if slices.Contains(got, name) {
			t.Errorf("a planning member got %s from its list", name)
		}
	}
}

// An update that changes a list rebuilds the member's view, and the member
// keeps its seat and its session.
func TestAToolsChangeRebuildsTheViewInPlace(t *testing.T) {
	w, s, _ := jevTools(t, "    tools: [read]\n")
	ctx := t.Context()
	cwd := w.cwd
	update := func(jev string) {
		t.Helper()
		if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Text: toolsCrew(cwd, jev)}); err != nil {
			t.Fatal(err)
		}
		if got := memberView(t, w, "crew", "jev").Session; got != s.id {
			t.Fatalf("jev moved from session %s to %s", s.id, got)
		}
	}
	update("    tools: [read, grep]\n")
	if got := toolNames(s); !slices.Contains(got, "grep") || slices.Contains(got, "glob") {
		t.Fatalf("after the list grew, jev has %v", got)
	}
	update("")
	if got := toolNames(s); !slices.Contains(got, "glob") {
		t.Fatalf("after the list was lifted, jev has %v", got)
	}
}

// Each shipped backend says whether a list can reach it, and a roster that
// gives a list to one that cannot is refused before it runs.
func TestAWorkerListNeedsABackendThatNarrows(t *testing.T) {
	env := talkootEnv()
	if err := env.DriverTools("claude", []string{"read", "bash"}); err != nil {
		t.Errorf("claude refused a list it can pass: %v", err)
	}
	for driver, list := range map[string][]string{"claude": {"mcp_github_*"}, "terva": {"read"}, "terva:portable": {"read"}} {
		if err := env.DriverTools(driver, list); err == nil {
			t.Errorf("%s took %v", driver, list)
		}
	}
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	_, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: toolsCrew(cwd, "    driver: terva\n    tools: [read]\n")})
	if err == nil || !strings.Contains(err.Error(), `driver "terva" cannot narrow its tools`) {
		t.Fatalf("want the roster refused, got %v", err)
	}
}

// 🚨 A seat whose member left the roster narrows to the seat alone. An
// update stores the new roster before it unseats the leaver, and a rebuild
// in that window must not widen it.
func TestALeavingMemberDoesNotWiden(t *testing.T) {
	w, s, _ := jevTools(t, "    tools: [read]\n")
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	r, err := talkoot.Parse([]byte("---\nname: crew\nhome: "+w.cwd+"\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n---\n"), "crew")
	if err != nil {
		t.Fatal(err)
	}
	run.roster.Store(&r)
	if got := w.talkootToolsOf(s.id); got == nil || len(got) != 0 {
		t.Fatalf("a seat with no member gave %#v, want an empty list", got)
	}
	s.rebuildTools("test")
	for _, name := range toolNames(s) {
		if !slices.Contains(talkoot.SeatTools, name) {
			t.Errorf("the leaving member has %s", name)
		}
	}
}

// The filter reads each tool's group, so mcp:github keeps the github
// server's tools and not those of a server whose name starts github_.
func TestAMemberFilterNamesOneServerByItsGroup(t *testing.T) {
	keep := memberToolFilter([]string{"read", "mcp:github"})
	for _, tc := range []struct {
		tool ctxFakeTool
		want bool
	}{
		{ctxFakeTool{name: "read"}, true},
		{ctxFakeTool{name: "bash"}, false},
		{ctxFakeTool{name: "mcp_github_issue", ext: "mcp:github"}, true},
		{ctxFakeTool{name: "mcp_github_x_issue", ext: "mcp:github_x"}, false},
		{ctxFakeTool{name: "talkoot_send"}, true},
		// A seat name from an extension is not the seat.
		{ctxFakeTool{name: "talkoot_send", ext: "evil"}, false},
	} {
		if got := keep(tc.tool.name, tc.tool); got != tc.want {
			t.Errorf("%s (%s): kept %v, want %v", tc.tool.name, tc.tool.ext, got, tc.want)
		}
	}
}
