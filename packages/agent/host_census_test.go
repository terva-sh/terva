package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A host is a place that builds a core.Agent and runs turns on it: the TUI, the
// web daemon's sessions, acp, rpc, bot mode, swarm children, the SDK, the
// deliberation panel's clerks. There are eight of them today and the number has
// only ever gone up.
//
// Every cross-host rule this repository holds was learned the same way. An
// invariant that spans hosts gets fixed in the one host a bug was reported
// against and stays broken in the others, because nothing enumerates them. The
// trust flip is the worked example: ACP shipped a /trust verb that moved one
// surface out of four, and each of the other three read as working code.
//
// The guards written afterwards recognise hosts by a hand-written list of
// FILENAMES. A list of names is not a census — it cannot fail when a host is
// ADDED, only when one already on it changes, and "a host was added" is the
// case every one of these bugs actually was. It is the same shape as a path
// string in a config file with nothing joining it to the repository, which cost
// three separate fixes in one release cut.
//
// So this file discovers the hosts by what they CALL, and the hand-written
// lists are joined to what it finds. It reads source rather than linking
// against anything, which is the same trade tool_rebuild_twin_test.go and
// trust_apply_twin_test.go make and for the same reason: a host's wiring lives
// in closures over locals of functions that spawn subprocesses before line one.
// Reading source also means build tags do not hide a host — acp_mode.go is
// behind terva_acp and only `just ci-acp` runs its behaviour tests, but its
// posture is censused on every run.

// repoRoot is this package's path back to the checkout root. The census walks
// the whole tree, not packages/agent: WHERE a host lives is exactly the thing
// that moves (the Q8 decomposition moved several), so rooting the walk at the
// directory hosts happen to sit in today would rebuild the same blind spot one
// level up.
const repoRoot = "../.."

// Which directories a walk stays out of is testsupport.SkipScanDir's business,
// not this file's. Five guards already share it, and its own doc comment records
// what happened to the one that was written from scratch instead: it descended
// into the worktrees under .claude and reported every file in them, failing
// `just ci` on a checkout whose own sources were clean.
//
// This file exists because guards drift when each one keeps its own list. It
// would be a poor place to keep a sixth.
//
// Nothing it prunes holds a host, and nothing it prunes is a path the release
// overlay strips from under this guard's feet — so unlike the docs-overlay
// checks, this census reads the same tree and gives the same answer in the
// published repo as it does here. A guard that cannot run where it lands is a
// guard that stopped.

// censusFloor is the vacuity guard. Every check below is "no file does X", which
// passes perfectly when the walk finds no files at all — the failure mode that
// makes a guard look green for years. 646 non-test .go files exist today; a walk
// that finds fewer than 400 has broken, not shrunk.
const censusFloor = 400

var (
	constructsAgent   = regexp.MustCompile(`\.NewAgent\(`)
	constructsDirect  = regexp.MustCompile(`core\.NewAgent\(`)
	definesRoot       = regexp.MustCompile(`func \(r Resolved\) NewAgent\(`)
	swapsToolSet      = regexp.MustCompile(`\.SetTools\(`)
	usesSharedRebuild = regexp.MustCompile(`LiveToolSet\{`)
	appliesTrust      = regexp.MustCompile(`\bApplyTrust\(`)
	definesApplyTrust = regexp.MustCompile(`^func ApplyTrust\(`)
	// The ASSIGNMENT, not the mention: acp_mode.go carries a comment explaining
	// why it does not pin, and a substring match would read that as pinning.
	pinsTrust = regexp.MustCompile(`\.TrustPin\s*=`)
)

// hostFile is one source file and what it does to an agent.
type hostFile struct {
	path   string   // repo-relative, slash-separated
	code   []string // non-empty, non-comment lines
	root   bool     // defines Resolved.NewAgent — the composition root itself
	builds bool     // constructs a core.Agent
	direct bool     // ...bypassing Resolved.NewAgent
	// rebuilds means the model's tool set is re-derived after launch. This is
	// the sharp edge for trust: a rebuild re-runs Resolve, and Resolve reads the
	// trust store, so a host that rebuilds picks up a verdict change whether it
	// meant to or not. Merely calling build.Resolve again is not enough to
	// qualify — the resumed-model path does that and swaps only the client.
	rebuilds bool
	live     bool // applies a trust flip through the shared event
	pins     bool // pins the verdict for the process's lifetime
}

func (h hostFile) underAgent() string { return strings.TrimPrefix(h.path, "packages/agent/") }

// census walks the tree and reports what every non-test Go file does.
func census(t *testing.T) []hostFile {
	t.Helper()
	var files []hostFile
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(repoRoot, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(path), repoRoot+"/"))
		h := hostFile{path: rel}
		for _, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			// Comment lines are dropped wholesale: half the matches in this
			// tree are prose ABOUT these calls, and a guard that fires on its
			// own explanation teaches people to delete the explanation.
			if code == "" || strings.HasPrefix(code, "//") {
				continue
			}
			h.code = append(h.code, code)
			if definesRoot.MatchString(code) {
				h.root = true
			}
			if constructsAgent.MatchString(code) {
				h.builds = true
				if constructsDirect.MatchString(code) {
					h.direct = true
				}
			}
			if swapsToolSet.MatchString(code) || usesSharedRebuild.MatchString(code) {
				h.rebuilds = true
			}
			// A declaration is not a call: build/trustapply.go DEFINES
			// ApplyTrust, which would otherwise census as the most
			// trust-applying host in the tree. Excluding that one line
			// specifically, rather than every line starting with `func` —
			// a probe caught this — because a single-line function body
			// carrying the call is still a call.
			if appliesTrust.MatchString(code) && !definesApplyTrust.MatchString(code) {
				h.live = true
			}
			if pinsTrust.MatchString(code) {
				h.pins = true
			}
		}
		files = append(files, h)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(files) < censusFloor {
		t.Fatalf("the census walked only %d non-test .go files (floor %d) — the walk is broken, and every "+
			"check in this file would have passed for the wrong reason", len(files), censusFloor)
	}
	return files
}

// agentHosts are the files that build an agent, minus the composition root
// itself. The root is found by the function it defines rather than by its name,
// so moving build.go does not silently promote it to a host.
func agentHosts(t *testing.T) []hostFile {
	t.Helper()
	var hosts []hostFile
	var roots int
	for _, f := range census(t) {
		if f.root {
			roots++
			continue
		}
		if f.builds {
			hosts = append(hosts, f)
		}
	}
	if roots != 1 {
		t.Fatalf("found %d files defining Resolved.NewAgent, want exactly 1 — the composition root has been "+
			"renamed, duplicated, or deleted, and this census can no longer tell a host from the root", roots)
	}
	if len(hosts) == 0 {
		t.Fatal("the census found no agent hosts at all, which cannot be true")
	}
	return hosts
}

// fixedTrustHosts records, for every agent host that does NOT apply a live
// trust flip, why "the verdict resolved at launch is the verdict this host
// keeps" is the right answer for it.
//
// The point is not the prose. It is that adding a host now requires ANSWERING
// this question, because the join below fails until the host appears here or
// applies the flip. ACP's broken /trust verb existed because nobody was ever
// asked; the posture was whatever the wiring happened to do.
var fixedTrustHosts = map[string]string{
	"packages/agent/cli.go": "the interactive and print hosts. /trust persists the verdict and says so — " +
		"modes.Interactive.TrustAppliesLive is false here, so the TUI prints the restart note instead of " +
		"claiming an apply it did not make. Nothing re-derives this host's tool set after launch (the " +
		"resumed-model path re-resolves but swaps only the client), so the launch verdict is the only one " +
		"it can be running.",
	"packages/agent/rpc.go": "fixed for the process's lifetime and PINNED so that stays true: rpc rebuilds " +
		"its tool set on an extension reload, and a rebuild re-resolves. Without Args.TrustPin a trust " +
		"change made in another process would arrive halfway — on the tools, not on the hooks — which is a " +
		"worse posture than either consistent answer.",
	"packages/agent/sdk/sdk.go": "an embedder's Runtime resolves once at New and never again: no reload " +
		"path, no trust verb on the API surface. An embedder that wants a different verdict builds another " +
		"Runtime, which is the same answer the CLI gives by asking for a restart.",
	"packages/agent/botcmd.go": "bot mode's chat surface carries no trust verb and the daemon has no reload " +
		"path; it runs against the workspace it was launched in until it is restarted.",
	"packages/agent/swarm_agent.go": "a swarm child is launched by a parent turn and lives for one task. It " +
		"inherits the verdict its parent resolved with and finishes before that verdict can move.",
	"packages/agent/workspace/workspace_raati.go": "the deliberation panel's summarizer and clerks are " +
		"tool-less throwaway agents — no tool registry, no hook engine, no extension manager — so there is " +
		"no trust-gated surface for a flip to reach.",
}

// Every agent host either applies the flip or has said why it does not. Both
// directions: an entry naming a file that stopped being a host, or that grew a
// live flip, is a stale decision and reads as a considered one.
func TestEveryAgentHostHasARecordedTrustPosture(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range agentHosts(t) {
		seen[h.path] = true
		_, recorded := fixedTrustHosts[h.path]
		switch {
		case h.live && recorded:
			t.Errorf("%s applies a live trust flip but is also recorded as fixed-trust — delete the entry, "+
				"it describes a posture this host no longer has", h.path)
		case !h.live && !recorded:
			t.Errorf("%s builds an agent but neither applies a trust flip nor records why it does not.\n"+
				"  Workspace Trust gates this host's hooks, its project extensions, its skills and its keyed "+
				"lore. Whether a mid-session change reaches them is a decision, and every host that has taken "+
				"it silently so far took it wrong.\n"+
				"  Either wire build.ApplyTrust, or add an entry to fixedTrustHosts saying what keeps the "+
				"launch verdict true here.", h.path)
		}
	}
	for path := range fixedTrustHosts {
		if !seen[path] {
			t.Errorf("fixedTrustHosts names %s, which no longer builds an agent — delete the entry rather "+
				"than leaving a decision standing for a host that is gone", path)
		}
	}
}

// The rule #476 was: a host that re-derives its tool set after launch is a host
// whose trust verdict can move under it, because the rebuild re-runs Resolve and
// Resolve reads the trust store. Such a host must either track the verdict
// deliberately (ApplyTrust) or pin it (Args.TrustPin). What it must not do is
// pick one up by accident on the next extension reload — half a flip, arriving
// at a moment nobody chose.
func TestAHostThatRebuildsItsToolSetTracksTheVerdictOrPinsIt(t *testing.T) {
	var checked int
	for _, h := range agentHosts(t) {
		if !h.rebuilds {
			continue
		}
		checked++
		if !h.live && !h.pins {
			t.Errorf("%s rebuilds its tool set after launch but neither applies trust live nor pins it.\n"+
				"  A rebuild re-resolves, and Resolve reads the trust store — so the next extension reload "+
				"silently adopts whatever verdict is on disk by then, for the tools only. The hooks, being "+
				"built once, keep the launch answer.\n"+
				"  Wire build.ApplyTrust, or set Args.TrustPin on the rebuild's args as rpc does.", h.path)
		}
	}
	if checked == 0 {
		t.Error("no agent host was seen rebuilding its tool set — three do, so the patterns this reads " +
			"(SetTools, LiveToolSet) have gone stale and the rule is checking nothing")
	}
}

// The steps ApplyTrust owns must not ALSO be performed by a host. Each of these,
// done outside the shared event, is done in isolation from the other three, and
// its ordering relative to them becomes accidental.
//
// This walks the whole tree. The version it replaces named eight files, and the
// files it did not name included every host that had never been examined — the
// SDK, bot mode, the deliberation clerks.
//
// `except` separates a FLIP from a launch SEED: every host tells its freshly
// built manager the verdict it resolved with, which is not this event. The seed
// is recognisable because its argument is the Resolved's own field.
var trustStepsOwnedByApplyTrust = []struct {
	pattern *regexp.Regexp
	except  *regexp.Regexp
	why     string
}{
	{
		pattern: regexp.MustCompile(`HookSpecsFor\(`),
		why: "swapping hook specs is step 1 of the flip; a host doing it alone decides for itself whether " +
			"the repo stops executing before or after it stops being visible, which is the ordering bug the daemon had",
	},
	{
		pattern: regexp.MustCompile(`\.SetProjectTrusted\(`),
		except:  regexp.MustCompile(`\.SetProjectTrusted\(r\.Trusted\)`),
		why: "flipping the extension gate has to be paired with the reload that acts on it, which is what " +
			"ApplyTrust does — a bare flip changes nothing until something else reloads, so it reads as applied " +
			"when it is not (the TUI carried exactly this for three verbs)",
	},
}

// trustStepHome is where these steps legitimately live: the shared event, and
// the file defining the helper it calls.
var trustStepHome = map[string]bool{
	"packages/agent/build/trustapply.go": true,
	"packages/agent/build/wiring.go":     true,
}

func TestNoTrustStepHappensOutsideTheSharedEvent(t *testing.T) {
	var checked int
	for _, f := range census(t) {
		if trustStepHome[f.path] {
			continue
		}
		for _, code := range f.code {
			for _, step := range trustStepsOwnedByApplyTrust {
				if !step.pattern.MatchString(code) {
					continue
				}
				if step.except != nil && step.except.MatchString(code) {
					checked++ // an excused launch seed still proves the pattern matches real code
					continue
				}
				t.Errorf("%s performs a trust-flip step outside build.ApplyTrust:\n    %s\n  %s", f.path, code, step.why)
			}
		}
	}
	if checked == 0 {
		t.Error("no trust-step spelling matched anywhere, not even the launch seeds every host has — the " +
			"patterns have gone stale and this guard is now checking nothing")
	}
}

// toolSetSwapHome is the two rebuild implementations that legitimately swap a
// tool set onto a running agent. Both are governed by the survivor rule in
// tool_rebuild_twin_test.go, which holds them in step with each other; this
// check is the outer fence — a THIRD implementation would carry a third copy of
// the survivor list, and every rebuild-survivor bug so far shipped exactly that
// way.
var toolSetSwapHome = map[string]string{
	"packages/agent/build/toolrebuild.go":           "build.LiveToolSet.Rebuild, the shared implementation rpc and acp both call",
	"packages/agent/workspace/workspace_session.go": "the daemon's own rebuildTools — it re-binds front-end channels and folds in workspace-only tools, neither of which exists off the daemon",
}

func TestNoHostSwapsAToolSetOutsideTheTwoRebuilds(t *testing.T) {
	var checked int
	found := map[string]bool{}
	for _, f := range census(t) {
		for _, code := range f.code {
			if !swapsToolSet.MatchString(code) {
				continue
			}
			found[f.path] = true
			if _, ok := toolSetSwapHome[f.path]; ok {
				checked++
				continue
			}
			t.Errorf("%s swaps a tool set onto a running agent:\n    %s\n  The survivor list — everything a "+
				"fresh Resolve mints anew and a running agent must keep — belongs in build.LiveToolSet, not in a "+
				"host. Call it, or add this file to toolSetSwapHome with what it does that the shared helper cannot.", f.path, code)
		}
	}
	for path := range toolSetSwapHome {
		if !found[path] {
			t.Errorf("toolSetSwapHome names %s, which no longer swaps a tool set — delete the entry", path)
		}
	}
	if checked == 0 {
		t.Error("no tool-set swap matched anywhere, not even the two known rebuilds — the pattern has gone stale")
	}
}

// directAgentConstruction: an agent built with core.NewAgent instead of
// Resolved.NewAgent takes none of the resolved defaults — and, more to the
// point, none of the ones added later. Resolved.NewAgent is where the read-only
// set, the auto-compact policy, lazy tool visibility, the escalation channel and
// the output cap are bound; each arrived after some host had already been
// written, and each reached every host that goes through it for free.
var directAgentConstruction = map[string]string{
	"packages/agent/workspace/workspace_raati.go": "tool-less summarizer and clerk turns that deliberately " +
		"take no resolved defaults: no tool registry to bind a status tool into, no session to adopt, no gate " +
		"to install. What they DO inherit by omission is core's zero values — MaxTokens is the live example, " +
		"where zero lets each provider apply its own cap (Bedrock's is 4096). Fine for a 400-word brief; " +
		"re-read this line before asking these agents for anything longer.",
}

func TestEveryAgentIsBuiltThroughTheCompositionRoot(t *testing.T) {
	for _, h := range agentHosts(t) {
		reason, excused := directAgentConstruction[h.path]
		switch {
		case h.direct && !excused:
			t.Errorf("%s builds an agent with core.NewAgent directly, bypassing Resolved.NewAgent — it will "+
				"not receive anything that is wired there today or added there tomorrow. Use r.NewAgent(), or "+
				"record here what this agent is that it needs none of it.", h.path)
		case !h.direct && excused:
			t.Errorf("%s is excused from the composition root (%q) but no longer bypasses it — delete the "+
				"stale exemption", h.path, reason)
		}
	}
}

// The live-trust hosts are named in trust_apply_twin_test.go, which asserts each
// one still reaches ApplyTrust. That list has the same weakness this file exists
// to remove — it cannot notice a host that was ADDED — so it is joined here to
// what the census actually finds.
//
// The list is still worth keeping as a list: it is the only thing that can fail
// when a host SILENTLY LOSES its live flip, because a host that no longer calls
// ApplyTrust is, to a census, simply a host that never did.
func TestTheLiveTrustHostListMatchesTheCensus(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range trustHosts {
		listed[name] = true
	}
	for _, f := range census(t) {
		if !f.live || !strings.HasPrefix(f.path, "packages/agent/") {
			continue
		}
		if !listed[f.underAgent()] {
			t.Errorf("%s applies a trust flip but is not in trustHosts — add it, so that the day it stops "+
				"applying one, something fails. A census cannot tell a host that never had a live flip from one "+
				"that lost it.", f.path)
		}
	}
}

// A host that stays up while its credentials age must keep them alive.
//
// The refresher exists because refresh used to be entirely demand-driven: it
// ran for the provider a turn was about to use, and for nothing else. A
// long-lived host therefore let every credential it was not using go stale, and
// the grant behind it eventually with it — recoverable only by a re-login the
// user did not know they needed until a turn failed.
//
// NewWorkspace starts one, which covers the TUI and the web daemon in a single
// wiring. It covers nothing else: acp and rpc bind core.Agent directly and
// never build a workspace, so they inherited nothing and nobody noticed until
// the question was asked out loud. That is the identical shape as the trust
// flip above — a cross-host invariant wired in whichever host it was reported
// against — and it gets the identical treatment.
//
// The list is written by ANSWERING, not by naming: a host either starts a
// refresher or records why its credentials cannot go stale under it.
var noRefresherHosts = map[string]string{
	"packages/agent/cli.go": "the print and json hosts, which resolve, run one prompt, and exit. " +
		"Interactive does not build its agent here — it routes to runInteractiveCtrlproto, which builds " +
		"the workspace, and the workspace starts the refresher for every session it holds. A one-shot " +
		"cannot outlive a refresh window, and starting a background sweep for a process about to exit " +
		"would spend a grant to no end.",
	"packages/agent/sdk/sdk.go": "an embedder's Runtime, which may well be long-lived — but a LIBRARY " +
		"that silently spawns a goroutine writing to the caller's auth.json is not a decision this " +
		"package gets to make for them. The embedder holds the process and can call authrefresh.Start " +
		"itself; what it must not get is a background writer it never asked for.",
	"packages/agent/swarm_agent.go": "a swarm child is launched by a parent turn and lives for one task. " +
		"It finishes long inside the refresh window of the credential it inherited, and its parent — the " +
		"TUI, the daemon, bot mode — is the host keeping that credential alive.",
	"packages/agent/workspace/workspace_raati.go": "the deliberation panel's summarizer and clerks are " +
		"throwaway agents built inside a live workspace, for the length of one deliberation. The " +
		"workspace that built them started the refresher.",
	"packages/agent/workspace/workspace_session.go": "every session the workspace holds. NewWorkspace " +
		"starts one refresher for all of them — per-session sweeps would be N processes' worth of " +
		"contention on one lock for a file that has one copy of each credential.",
}

var startsRefresher = regexp.MustCompile(`authrefresh\.Start\(`)

func TestEveryAgentHostHasARecordedCredentialRefreshPosture(t *testing.T) {
	starts := map[string]bool{}
	for _, f := range census(t) {
		for _, code := range f.code {
			if startsRefresher.MatchString(code) {
				starts[f.path] = true
			}
		}
	}
	// The workspace starts one for every host it builds, so a host that reaches
	// its agent through NewWorkspace is covered by that and not by its own call.
	// cli.go binds the workspace to the TUI; web_mode.go builds one too.
	seen := map[string]bool{}
	for _, h := range agentHosts(t) {
		seen[h.path] = true
		_, recorded := noRefresherHosts[h.path]
		switch {
		case starts[h.path] && recorded:
			t.Errorf("%s starts a credential refresher but is also recorded as not needing one — delete "+
				"the entry, it describes a posture this host no longer has", h.path)
		case !starts[h.path] && !recorded:
			t.Errorf("%s builds an agent but neither starts a credential refresher nor records why it "+
				"does not need one.\n"+
				"  A host that outlives a token's refresh window and does not refresh it lets the grant "+
				"behind it lapse, and the user finds out from a 401 on a provider they were not even "+
				"using.\n"+
				"  Either start authrefresh.Start, or add an entry to noRefresherHosts saying what keeps "+
				"this host's credentials fresh (it is short-lived; it inherits the workspace's; it holds "+
				"no credential at all).", h.path)
		}
	}
	for path := range noRefresherHosts {
		if !seen[path] {
			t.Errorf("noRefresherHosts names %s, which no longer builds an agent — delete the entry rather "+
				"than leaving a decision standing for a host that is gone", path)
		}
	}
}
