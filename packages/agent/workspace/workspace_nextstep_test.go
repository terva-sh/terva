package workspace

// Stage 1 of docs/proposals/idle-suggestions.md: one ephemeral completion that
// proposes the user's next line, against the session's own prefix, recording
// nothing.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// nextStepClient answers with a canned reply and records every request, so a
// test can inspect what was actually sent as well as what came back.
type nextStepClient struct {
	mu    sync.Mutex
	reqs  []provider.Request
	reply string
	usage provider.Usage
}

func (c *nextStepClient) Name() string { return "nextstep" }

func (c *nextStepClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	reply, usage := c.reply, c.usage
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "nextstep", Model: req.Model}
		out <- provider.EventTextDelta{Delta: reply}
		if usage != (provider.Usage{}) {
			out <- provider.EventUsage{Usage: usage}
		}
		out <- provider.EventDone{Stop: provider.StopEnd}
	}()
	return out, nil
}

func (c *nextStepClient) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func (c *nextStepClient) lastReq(t *testing.T) provider.Request {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		t.Fatal("no request reached the client")
	}
	return c.reqs[len(c.reqs)-1]
}

// nextStepSession builds a workspace with one live session: a system prompt, a
// seeded transcript, an ephemeral tail, and a recording client.
func nextStepSession(t *testing.T, id, reply string) (*Workspace, *wsSession, *nextStepClient) {
	t.Helper()
	w, s, _ := chatTestWorkspace(t, id)
	cl := &nextStepClient{reply: reply}
	ag := core.NewAgent(cl, "fake-model", "the session's own system prompt", core.Registry{})
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "what broke the build?"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a missing import in main.go"}}},
	})
	// Both halves wired, differently: the surface must reach for the
	// side-effect-free one. A suggestion the user may never see must not record
	// which lore entries fired on the session's behalf.
	ag.ContextProviderPeek = func() string { return "the peeked tail" }
	ag.ContextProvider = func() string { return "the recording tail" }
	s.agent = ag
	return w, s, cl
}

func sessionFilePath(t *testing.T, w *Workspace, id string) string {
	t.Helper()
	return filepath.Join(w.root, id+".jsonl")
}

func readSessionFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	return b
}

// The request is the session's own prefix — its system prompt, its transcript,
// its ephemeral tail — with the ask appended last. That shape is the whole
// reason this rides the side-chat pattern rather than the suggest one: the
// prefix is the session's, so the call reads the prompt cache instead of paying
// to re-read the conversation.
func TestNextStepAsksAgainstTheSessionsOwnPrefix(t *testing.T) {
	w, _, cl := nextStepSession(t, "s1", "run the tests")

	got, err := w.SuggestNextStep(context.Background(), "s1")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.Line != "run the tests" {
		t.Fatalf("suggestion = %q", got.Line)
	}

	req := cl.lastReq(t)
	if req.System != "the session's own system prompt" {
		t.Fatalf("system = %q, want the session's own", req.System)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("request carried %d messages, want 3 (2 transcript + the ask)", len(req.Messages))
	}
	if first := messageText(req.Messages[0]); first != "what broke the build?" {
		t.Fatalf("message[0] = %q, want the transcript to lead", first)
	}
	ask := req.Messages[2]
	if ask.Role != provider.RoleUser {
		t.Fatalf("the ask went out as %q; it has to be a user message to sit after the transcript", ask.Role)
	}
	if !strings.HasPrefix(messageText(ask), NextStepTag) {
		t.Fatalf("the ask does not lead with %s: %q", NextStepTag, messageText(ask))
	}
	// The prohibition comes before the request it governs — position is the
	// measured part (scripts/eval/README.md), so pin the order, not the wording.
	body := messageText(ask)
	doNot := strings.Index(body, "Do not")
	replyWith := strings.Index(body, "Reply with")
	if doNot < 0 || replyWith < 0 || doNot > replyWith {
		t.Fatalf("the ask must prohibit before it asks; got %q", body)
	}

	if req.EphemeralContext != "the peeked tail" {
		t.Fatalf("ephemeral tail = %q, want the PEEKED one — a suggestion must not "+
			"record lore activations on the session's behalf", req.EphemeralContext)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("the suggestion carried %d tools; it must not be able to act", len(req.Tools))
	}
	if req.MaxTokens != nextStepMaxTokens {
		t.Fatalf("MaxTokens = %d, want %d", req.MaxTokens, nextStepMaxTokens)
	}
	// Reasoning off, and EXPLICITLY off: without ReasoningSet the model's own
	// default wins, and on OpenAI's reasoning path the cap is spent thinking and
	// the call comes back empty.
	if req.Reasoning != "" || !req.ReasoningSet {
		t.Fatalf("Reasoning = %q, ReasoningSet = %v; want an explicit off",
			req.Reasoning, req.ReasoningSet)
	}
}

// It records nothing: not the ask, not the answer, not in memory and not on
// disk. The session file is the half that matters — an in-memory check alone
// would pass on a surface that wrote straight through to the log.
func TestNextStepRecordsNothing(t *testing.T) {
	w, s, _ := nextStepSession(t, "s1", "run the tests")
	path := sessionFilePath(t, w, "s1")
	before := readSessionFile(t, path)
	beforeMsgs := len(s.agent.Messages())

	if _, err := w.SuggestNextStep(context.Background(), "s1"); err != nil {
		t.Fatalf("suggest: %v", err)
	}

	if after := readSessionFile(t, path); !strings.EqualFold(string(after), string(before)) {
		t.Fatalf("the session file changed:\nbefore: %s\nafter:  %s", before, after)
	}
	if after := len(s.agent.Messages()); after != beforeMsgs {
		t.Fatalf("the transcript changed: %d -> %d", beforeMsgs, after)
	}

	// The other half of the claim: this fixture's session file DOES grow when
	// something records, so the assertion above is about the suggestion rather
	// than about a file nothing ever writes to.
	if err := s.sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "a real message"}},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if after := readSessionFile(t, path); len(after) <= len(before) {
		t.Fatal("the session file did not grow on a real append, so \"unchanged\" above proved nothing")
	}
}

// Each call re-reads the live transcript. A snapshot frozen once would answer
// the second call against the first call's conversation, which is the failure
// the proposal calls worse than no suggestion: it looks current.
func TestNextStepRereadsTheTranscriptOnEachCall(t *testing.T) {
	w, s, cl := nextStepSession(t, "s1", "run the tests")

	if _, err := w.SuggestNextStep(context.Background(), "s1"); err != nil {
		t.Fatalf("first suggest: %v", err)
	}
	if n := len(cl.lastReq(t).Messages); n != 3 {
		t.Fatalf("first call carried %d messages, want 3", n)
	}

	s.agent.SetMessages(append(s.agent.Messages(),
		provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "fixed it, what now?"}}}))

	if _, err := w.SuggestNextStep(context.Background(), "s1"); err != nil {
		t.Fatalf("second suggest: %v", err)
	}
	req := cl.lastReq(t)
	if len(req.Messages) != 4 {
		t.Fatalf("second call carried %d messages, want 4 (3 transcript + the ask)", len(req.Messages))
	}
	if got := messageText(req.Messages[2]); got != "fixed it, what now?" {
		t.Fatalf("message[2] = %q; the second call did not see the newer turn", got)
	}
}

// One line reaches the composer whatever the model does. The ask says so; this
// is what holds when it is ignored — and taking the FIRST line is the "never a
// plan" rule made structural rather than requested.
func TestNextStepReturnsOneShortLine(t *testing.T) {
	long := strings.Repeat("é", nextStepMaxRunes+50)
	for _, tc := range []struct{ name, reply, want string }{
		{"leading blank lines are skipped", "\n\n  run the tests  \n", "run the tests"},
		{"a plan contributes its first step only", "1. run the tests\n2. fix the failures\n3. push", "1. run the tests"},
		{"nothing to suggest stays empty", "   \n\t\n", ""},
		{"an overlong line is bounded", long, strings.Repeat("é", nextStepMaxRunes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _, _ := nextStepSession(t, "s1", tc.reply)
			got, err := w.SuggestNextStep(context.Background(), "s1")
			if err != nil {
				t.Fatalf("suggest: %v", err)
			}
			if got.Line != tc.want {
				t.Fatalf("suggestion = %q, want %q", got.Line, tc.want)
			}
		})
	}
}

// An empty session has no next step to name, and must not spend a completion
// being told so.
func TestNextStepOnAnEmptySessionNeverCallsTheModel(t *testing.T) {
	w, s, cl := nextStepSession(t, "s1", "run the tests")
	s.agent.SetMessages(nil)

	got, err := w.SuggestNextStep(context.Background(), "s1")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.Line != "" {
		t.Fatalf("suggestion = %q, want empty", got.Line)
	}
	if n := cl.calls(); n != 0 {
		t.Fatalf("%d request(s) reached the client; an empty session must cost nothing", n)
	}
}

// A credential-less boot has nothing to complete against, and says so rather
// than panicking on a nil client.
func TestNextStepWithoutACredentialRefuses(t *testing.T) {
	w, s, _ := nextStepSession(t, "s1", "run the tests")
	s.agent = core.NewAgent(nil, "fake-model", "", core.Registry{})

	if _, err := w.SuggestNextStep(context.Background(), "s1"); err == nil {
		t.Fatal("a session with no client should refuse, not suggest")
	}
}

// The spend is real even though nothing is recorded, so it is booked against
// the session like every other side-channel call.
func TestNextStepBooksItsSpend(t *testing.T) {
	w, s, cl := nextStepSession(t, "s1", "run the tests")
	cl.mu.Lock()
	cl.usage = provider.Usage{InputTokens: 900, OutputTokens: 7}
	cl.mu.Unlock()

	var booked provider.Usage
	s.agent.AddUsageObserver(func(u, _ provider.Usage) { booked = u })

	if _, err := w.SuggestNextStep(context.Background(), "s1"); err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if booked.OutputTokens != 7 || booked.InputTokens != 900 {
		t.Fatalf("booked usage = %+v, want the completion's own", booked)
	}
}
