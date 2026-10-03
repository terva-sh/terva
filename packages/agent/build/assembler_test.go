package build

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/contextpressure"
	"terva.sh/terva/packages/core/lazytools"
	"terva.sh/terva/packages/core/shellresult"
	"terva.sh/terva/packages/core/stall"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// SetStable's answer drives the prompt-rebuilt notice, which tells clients the
// prompt cache is about to miss. It must follow the bytes a provider caches:
// silent on an identical re-render, which a rebuild produces far more often
// than not, and silent on a retag, which changes nothing sent.
func TestSetStableReportsAChangeToTheBytesOnly(t *testing.T) {
	asm := NewAssembler([]PromptSegment{{Source: SourceIdentityIntro, Text: "You are terva."}, {Source: SourceFooter, Text: "cwd: /x"}})
	if asm.SetStable([]PromptSegment{{Source: SourceIdentityIntro, Text: "You are terva."}, {Source: SourceFooter, Text: "cwd: /x"}}) {
		t.Error("an identical re-render reported a change")
	}
	if asm.SetStable([]PromptSegment{{Source: "renamed", Text: "You are terva."}, {Source: SourceFooter, Text: "cwd: /x"}}) {
		t.Error("a retag with the same text reported a change")
	}
	if !asm.SetStable([]PromptSegment{{Source: SourceIdentityIntro, Text: "You are terva."}, {Source: SourceFooter, Text: "cwd: /y"}}) {
		t.Error("a different system prompt reported no change")
	}
	if got := asm.Assemble(core.AssemblePeek).SystemText(); got != "You are terva.\n\ncwd: /y" {
		t.Errorf("SystemText = %q", got)
	}
}

// The Stable segments carry the sections' sources as tags, so /context can name
// them, the tail is one Volatile segment tagged host, so the tail IDs recorded
// in sessions do not change, and the context-pressure and shell-result
// segments follow it.
func TestTheAssemblersFrameShape(t *testing.T) {
	asm := NewAssembler([]PromptSegment{{Source: SourceIdentityIntro, Text: "intro"}, {Source: SourceAgentsMD, Text: "agents"}})
	requested, peeked := 0, 0
	asm.SetTail(func() string { requested++; return "tail" }, func() string { peeked++; return "tail" })

	f := asm.Assemble(core.AssembleRequest)
	var tags []string
	for _, s := range f.Segments {
		tags = append(tags, s.Tag)
	}
	want := []string{SourceIdentityIntro, SourceAgentsMD, core.TailHost, contextpressure.ID, shellresult.ID, stall.ID}
	if strings.Join(tags, ",") != strings.Join(want, ",") {
		t.Errorf("tags = %v, want %v", tags, want)
	}
	if f.Segments[2].Stability != core.Volatile {
		t.Error("the tail is not Volatile")
	}
	asm.Assemble(core.AssemblePeek)
	if requested != 1 || peeked != 1 {
		t.Errorf("request called the tail %d times and the peek %d; each mode must call its own", requested, peeked)
	}

	// With no peek twin, a peek falls back to the tail itself.
	asm.SetTail(func() string { return "only" }, nil)
	if got := asm.Assemble(core.AssemblePeek).VolatileText(); got != "only" {
		t.Errorf("peek without a twin = %q", got)
	}
}

// The wiring that changes terva's frame refuses an agent without terva's
// assembler, rather than doing nothing and dropping the task card, the
// extension cards and the lore without a word.
func TestWiringATailOntoAForeignAgentPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("WireEphemeralTail on an agent without a build.Assembler did not panic")
		}
	}()
	WireEphemeralTail(coretest.NewAgentWithAssembler(nil, "m", core.StaticSystem("sys"), nil), EphemeralTail{})
}

// tailClient keeps each request's ephemeral tail. With hold set it then waits
// to be cancelled, the shape a withdrawal needs; otherwise it answers.
type tailClient struct {
	hold    bool
	tails   chan string
	started chan struct{}
}

func (c *tailClient) Name() string { return "tail-fake" }

func (c *tailClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.tails <- req.EphemeralContext
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		if c.hold {
			c.started <- struct{}{}
			<-ctx.Done()
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// NewAgent attaches the shell-result slot to the agent it builds. Only a
// withdrawal shows it: the slot's segment and its delivery ride the assembler,
// so without the attachment a result still reaches the model once, and is then
// lost when the prompt that carried it is taken back.
func TestNewAgentAttachesTheShellResultSlot(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, true)
	if err != nil {
		t.Fatal(err)
	}
	a := r.NewAgent(core.AllowAll)
	slot := AssemblerOf(a).ShellResult()
	slot.SetEnabled(true)
	slot.Set("git status", "3 files changed")

	held := &tailClient{hold: true, tails: make(chan string, 4), started: make(chan struct{}, 1)}
	a.SetClientAndModel(held, "", "fake-model")
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Prompt(ctx, "waht sould I comit", nil, func(core.AgentEvent) {})
	}()
	select {
	case <-held.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	cancel(core.ErrUserInterrupted)
	<-done
	if got := <-held.tails; !strings.Contains(got, "3 files changed") {
		t.Fatalf("the withdrawn request never carried the result, so this proves nothing:\n%s", got)
	}

	answering := &tailClient{tails: make(chan string, 4)}
	a.SetClientAndModel(answering, "", "fake-model")
	if err := a.Prompt(context.Background(), "what should I commit first?", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatal(err)
	}
	if got := <-answering.tails; !strings.Contains(got, "3 files changed") {
		t.Errorf("the withdrawal lost the shell result; the slot is not attached to the agent:\n%s", got)
	}
}

// NewAgent attaches the context-pressure tracker, and the note it carries is
// worded by terva's CompactionPolicy rather than the engine's neutral default.
// Without the attachment the tracker has no gauge and the note never rides.
func TestNewAgentCarriesTervasPressureNote(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, true)
	if err != nil {
		t.Fatal(err)
	}
	a := r.NewAgent(core.AllowAll)
	client := &tailClient{tails: make(chan string, 4)}
	a.SetClientAndModel(client, "", "claude-sonnet-4-5")
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 150_000}) // 75% of 200k

	if err := a.Prompt(context.Background(), "hello", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatal(err)
	}
	got := <-client.tails
	if !strings.Contains(got, "[context pressure]") {
		t.Fatalf("the request carried no pressure note at 75%%; the tracker is not attached:\n%s", got)
	}
	if !strings.Contains(got, "Complete the request of the user as if the note were not here") {
		t.Errorf("the note is not terva's wording, so CompactionPolicy.PressureNote was not asked:\n%s", got)
	}
}

// With lazy tools on, the inactive-group note is the frame's last segment,
// after the other component notes, as it was when the engine appended it. And
// terva's delivery report reaches it: the note decays to its one-line form
// once three requests have carried it in full.
func TestTheAssemblerCarriesTheLazyNoteAndItsDecay(t *testing.T) {
	reg := core.Registry{
		"read":      plainTool{name: "read"},
		"mail_send": groupedTool{plainTool: plainTool{name: "mail_send"}, group: "mail"},
	}
	client := &ticketScriptClient{}
	asm := NewAssembler([]PromptSegment{{Source: SourceIdentityIntro, Text: "intro"}})
	opts := append([]core.Option{core.WithAssembler(asm), core.WithTools(reg), core.WithGate(core.AllowAll)}, LazyTools(asm)...)
	a, err := core.New(client, "m", opts...)
	if err != nil {
		t.Fatal(err)
	}

	f := asm.Assemble(core.AssemblePeek)
	if last := f.Segments[len(f.Segments)-1]; last.Tag != lazytools.NoteFull || !strings.Contains(last.Content, "mail_send") {
		t.Fatalf("the last segment is %q with %q, want the full note naming mail_send", last.Tag, last.Content)
	}
	for i := 0; i < 4; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	for i, tail := range client.ephemeral {
		full := strings.Contains(tail, "mail_send")
		if want := i < 3; full != want {
			t.Errorf("request %d carried the full inventory: %v, want %v (tail %q)", i, full, want, tail)
		}
		if !strings.Contains(tail, "[inactive tool groups]") {
			t.Errorf("request %d carried no note: %q", i, tail)
		}
	}
}
