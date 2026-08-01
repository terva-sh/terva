package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// said builds an agent whose most recent user message is text.
func said(text string) *core.Agent {
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}},
	})
	return ag
}

// This is the whole feature in one assertion. The two tiers must land on
// opposite sides of the prompt cache: active memory in the cached system prefix
// on every request, archived memory absent from it entirely and appearing in the
// uncached per-turn tail only when the conversation reaches it.
//
// Both directions are checked, and the negative ones are the ones that matter. An
// archived entry leaking into the block would mean paying prefix bytes for
// everything ever archived — the exact cost the tier exists to avoid — and an
// active entry leaking into the tail would mean paying for it twice on every
// turn, in the cached copy and the uncached one.
func TestTheTwoTiersLandOnOppositeSidesOfTheCache(t *testing.T) {
	r, mt := memResolved(t)
	if err := mt.Project.Add("uses pnpm, not npm"); err != nil {
		t.Fatal(err)
	}
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "cutting a release",
		Keys: []string{"release", "ship", "tag"},
		Text: "Run `just release-status` first; it is the oracle. Burned tags cannot be reused.",
	}); err != nil {
		t.Fatal(err)
	}

	block := MemoryBlock(r)
	if !strings.Contains(block, "uses pnpm") {
		t.Errorf("the active entry is missing from the cached block:\n%s", block)
	}
	if strings.Contains(block, "release-status") {
		t.Errorf("an ARCHIVED entry reached the cached prefix — the tier saves nothing:\n%s", block)
	}

	tail := r.PerTurnContext(said("how do I cut a release?"))
	if tail == nil {
		t.Fatal("no per-turn tail was installed for a session with an archive")
	}
	got := tail()
	if !strings.Contains(got, "release-status") {
		t.Errorf("the archived entry did not reach the tail on a matching turn:\n%s", got)
	}
	if strings.Contains(got, "uses pnpm") {
		t.Errorf("an ACTIVE entry was duplicated into the tail; it is already in the prefix:\n%s", got)
	}

	// And on an unrelated turn the tail is silent, which is what makes the
	// archive free to grow.
	if got := r.PerTurnContext(said("rename this CSS class"))(); strings.Contains(got, "release-status") {
		t.Errorf("the archived entry fired on an unrelated turn:\n%s", got)
	}
}

// The recall block is framed for the same reason the lore block is, and against
// a hazard this tier has more of: a memory is written once and rarely revised,
// while the conversation is current, and the block lands after the entire
// transcript where recency alone reads as authority.
func TestRecalledMemoryIsFramedAsReferenceNotState(t *testing.T) {
	r, mt := memResolved(t)
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "the deploy target", Keys: []string{"deploy"}, Text: "Deploys go to staging first.",
	}); err != nil {
		t.Fatal(err)
	}
	got := r.PerTurnContext(said("let's deploy this"))()

	if !strings.Contains(got, "RECALLED FROM YOUR ARCHIVED MEMORY") {
		t.Fatalf("the recall block is unframed:\n%s", got)
	}
	if strings.Index(got, "RECALLED FROM") > strings.Index(got, "staging first") {
		t.Errorf("the frame must introduce the block it frames:\n%s", got)
	}
	// The tiebreak clause is the load-bearing half. Without it the block reads as
	// the present simply because it arrives last.
	if !strings.Contains(got, "the conversation is what is actually happening") {
		t.Errorf("the frame does not hand the tiebreak to the transcript:\n%s", got)
	}
	// The id has to travel with the content: this block is the only place a
	// recalled memory is visible, so without it the model cannot name what it
	// just read in order to correct or forget it.
	if !strings.Contains(got, "[project:the-deploy-target]") {
		t.Errorf("the recalled entry is unnamed, so nothing can act on it:\n%s", got)
	}
}

// The tail must stay live for a session that starts with an empty archive.
//
// Gating the provider on "is anything archived yet" was the tempting
// optimisation and it is wrong in exactly the case that matters: the first
// session anyone uses this feature in begins with an empty archive, so the tier
// would be dead for the whole of it — archive at turn three, fire never. The
// closure reads the archive live, so it costs an empty string per turn instead.
func TestTheTailStaysLiveForASessionThatArchivesLater(t *testing.T) {
	r, mt := memResolved(t)
	tail := r.PerTurnContext(said("anything about backups?"))
	if tail == nil {
		t.Fatal("no tail installed for a session whose archive is empty at build time")
	}
	if got := tail(); got != "" {
		t.Fatalf("an empty archive rendered %q, want nothing", got)
	}

	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "backups", Keys: []string{"backup", "backups"}, Text: "Backups run nightly at 02:00 UTC.",
	}); err != nil {
		t.Fatal(err)
	}
	if got := tail(); !strings.Contains(got, "02:00 UTC") {
		t.Errorf("an entry archived mid-session did not fire on the next turn:\n%s", got)
	}
}

// Memory off, or an extension holding the slot, means no recall — the same
// binary the block already honours. A session that opted out of memory must not
// grow a second, keyed channel that still injects.
func TestNoRecallWithoutCoresMemoryTool(t *testing.T) {
	if got := (&Resolved{ToolRegistry: core.Registry{}}).MemoryRecall(said("x"), true); got != nil {
		t.Error("a registry with no memory tool still installed a recall provider")
	}
	foreign := &Resolved{ToolRegistry: core.Registry{"memory": &tools.AskUserTool{}}}
	if got := foreign.MemoryRecall(said("x"), true); got != nil {
		t.Error("an extension-provided memory tool still installed core's recall provider")
	}

	// And standing down for an extension takes recall with it, because both read
	// the same registry entry.
	r, mt := memResolved(t)
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "x", Keys: []string{"widget"}, Text: "a fact about widgets",
	}); err != nil {
		t.Fatal(err)
	}
	if r.MemoryRecall(said("the widget"), true) == nil {
		t.Fatal("precondition: recall should be live before the stand-down")
	}
	r.standDownForExtensionMemory(fakeExtSource{names: []string{"memory"}})
	if r.MemoryRecall(said("the widget"), true) != nil {
		t.Error("recall survived the stand-down; two memories are live at once")
	}
}

// The peek twin sizes the tail for /context. It has to include recalled memory
// or the size view undercounts every turn on which the archive fires — the same
// defect the ephemeral-tail wiring already had once, where /context missed the
// extension cards.
func TestPeekSizesTheRecalledBlockToo(t *testing.T) {
	r, mt := memResolved(t)
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "budget", Keys: []string{"budget"}, Text: "The tail budget is measured in approximate tokens.",
	}); err != nil {
		t.Fatal(err)
	}
	ag := said("what is the budget?")
	live, peek := r.PerTurnContext(ag), r.PerTurnContextPeek(ag)
	if peek == nil {
		t.Fatal("no peek provider was installed")
	}
	if live() != peek() {
		t.Errorf("peek does not match the live tail:\nlive: %q\npeek: %q", live(), peek())
	}
	if !strings.Contains(peek(), "approximate tokens") {
		t.Errorf("peek undercounts the recalled block:\n%s", peek())
	}
}

// The activation trace records what the LAST TURN recalled, and only the
// recording tail may write it.
//
// The sizing twin behind /context renders the identical block against whatever
// the transcript looks like at the moment someone opens a size view. If it also
// recorded, opening /context would overwrite the trace of the turn that actually
// ran with one computed from a request nobody sent — and the pane would then
// explain the model's context using entries it never saw. Same contract
// LoreFiredRecord keeps, for the same reason.
func TestPeekSizesTheTailWithoutRecordingTheTrace(t *testing.T) {
	r, mt := memResolved(t)
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "the gate", Keys: []string{"gate"}, Text: "CI runs go test -race.",
	}); err != nil {
		t.Fatal(err)
	}
	ag := said("what about the gate?")

	if got := r.PerTurnContextPeek(ag)(); !strings.Contains(got, "go test -race") {
		t.Fatalf("peek did not render the recalled block:\n%s", got)
	}
	if trace := mt.LastRecall(); len(trace) != 0 {
		t.Errorf("peek recorded a trace: %+v", trace)
	}

	// The real tail does record, with the key that fired it — which is the whole
	// answer to "why is this in my context".
	if got := r.PerTurnContext(ag)(); !strings.Contains(got, "go test -race") {
		t.Fatalf("the live tail did not render the recalled block:\n%s", got)
	}
	trace := mt.LastRecall()
	if len(trace) != 1 {
		t.Fatalf("trace = %+v, want the one entry that fired", trace)
	}
	if trace[0].Ref != "project:the-gate" || trace[0].Dropped {
		t.Errorf("trace entry = %+v", trace[0])
	}
	if len(trace[0].Keys) != 1 || trace[0].Keys[0] != "gate" {
		t.Errorf("trace did not record WHY it fired: %v", trace[0].Keys)
	}
}

// Both scopes are matched together, so a user-scoped memory reaches a project
// conversation and vice versa. They are different subjects, not different
// conversations — which is why they share one budget rather than one each.
func TestBothScopesRecallIntoOneBlock(t *testing.T) {
	r, mt := memResolved(t)
	if _, err := mt.UserArchive.Add(memory.ArchiveEntry{
		Name: "review style", Keys: []string{"review"}, Text: "Drew wants the evidence before the conclusion.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mt.ProjectArchive.Add(memory.ArchiveEntry{
		Name: "review gate", Keys: []string{"review"}, Text: "CI runs go test -race; just ci does not.",
	}); err != nil {
		t.Fatal(err)
	}
	got := r.PerTurnContext(said("start a review"))()
	for _, want := range []string{"evidence before the conclusion", "go test -race", "[user:review-style]", "[project:review-gate]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from the recalled block:\n%s", want, got)
		}
	}
}
