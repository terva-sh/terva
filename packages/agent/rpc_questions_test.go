package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

type rpcQuestionFrameWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	frames chan []byte
}

func newRPCQuestionFrameWriter() *rpcQuestionFrameWriter {
	return &rpcQuestionFrameWriter{frames: make(chan []byte, 128)}
}

func (w *rpcQuestionFrameWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	var frames [][]byte
	for {
		line := w.buf.Bytes()
		at := bytes.IndexByte(line, '\n')
		if at < 0 {
			break
		}
		frames = append(frames, append([]byte(nil), line[:at]...))
		w.buf.Next(at + 1)
	}
	w.mu.Unlock()
	for _, frame := range frames {
		w.frames <- frame
	}
	return len(p), nil
}

type rpcQuestionBarrierWriter struct {
	inner          *rpcQuestionFrameWriter
	secondQuestion chan struct{}
	questionCount  int
	mu             sync.Mutex
}

func (w *rpcQuestionBarrierWriter) Write(p []byte) (int, error) {
	var frame map[string]any
	if json.Unmarshal(bytes.TrimSpace(p), &frame) == nil && frame["type"] == "question" {
		w.mu.Lock()
		w.questionCount++
		if w.questionCount == 2 {
			close(w.secondQuestion)
		}
		w.mu.Unlock()
	}
	return w.inner.Write(p)
}

func nextRPCQuestionFrame(t *testing.T, w *rpcQuestionFrameWriter) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(<-w.frames, &frame); err != nil {
		t.Fatalf("decode rpc frame: %v", err)
	}
	return frame
}

func sendRPCQuestionCommand(t *testing.T, in *io.PipeWriter, command map[string]any) {
	t.Helper()
	data, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(in, "%s\n", data); err != nil {
		t.Fatal(err)
	}
}

func sendRPCQuestionLine(t *testing.T, in *io.PipeWriter, line string) {
	t.Helper()
	if _, err := fmt.Fprintln(in, line); err != nil {
		t.Fatal(err)
	}
}

type rpcQuestionRun struct {
	server *rpcServer
	in     *io.PipeWriter
	out    *rpcQuestionFrameWriter
	done   chan error
}

func startRPCQuestionRun(t *testing.T, capabilities map[string]bool) rpcQuestionRun {
	t.Helper()
	reader, writer := io.Pipe()
	out := newRPCQuestionFrameWriter()
	s := &rpcServer{ctx: context.Background(), out: out}
	done := make(chan error, 1)
	go func() { done <- s.run(reader) }()
	run := rpcQuestionRun{server: s, in: writer, out: out, done: done}
	if capabilities != nil {
		negotiateRPCQuestionCapability(t, run, capabilities)
	}
	return run
}

func startRPCQuestionBarrierRun(t *testing.T, barrier *rpcQuestionBarrierWriter) rpcQuestionRun {
	t.Helper()
	reader, writer := io.Pipe()
	s := &rpcServer{ctx: context.Background(), out: barrier}
	done := make(chan error, 1)
	go func() { done <- s.run(reader) }()
	return rpcQuestionRun{server: s, in: writer, out: barrier.inner, done: done}
}

func negotiateRPCQuestionCapability(t *testing.T, run rpcQuestionRun, capabilities map[string]bool) {
	t.Helper()
	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":           "hello-1",
		"type":         "hello",
		"capabilities": capabilities,
	})
	frame := nextRPCQuestionFrame(t, run.out)
	if frame["type"] != "response" || frame["success"] != true {
		t.Fatalf("hello failed: %#v", frame)
	}
	data, ok := frame["data"].(map[string]any)
	if !ok {
		t.Fatalf("hello response has no data: %#v", frame)
	}
	advertised, ok := data["capabilities"].(map[string]any)
	if !ok || advertised[rpcStructuredQuestionsCapability] != true {
		t.Fatalf("hello did not advertise structured questions: %#v", data)
	}
}

func (r rpcQuestionRun) close(t *testing.T) {
	t.Helper()
	if err := r.in.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
}

type rpcWireQuestion struct {
	ID          string   `json:"id"`
	Header      string   `json:"header"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	AllowCustom bool     `json:"allow_custom"`
	MultiSelect bool     `json:"multi_select"`
}

func questionBatch(t *testing.T, frame map[string]any) (string, []rpcWireQuestion) {
	t.Helper()
	if frame["type"] != "question" {
		t.Fatalf("frame type = %v, want question: %#v", frame["type"], frame)
	}
	id, ok := frame["id"].(string)
	if !ok || id == "" {
		t.Fatalf("question frame has no id: %#v", frame)
	}
	encoded, err := json.Marshal(frame["questions"])
	if err != nil {
		t.Fatal(err)
	}
	var questions []rpcWireQuestion
	if err := json.Unmarshal(encoded, &questions); err != nil {
		t.Fatalf("decode question batch: %v", err)
	}
	return id, questions
}

func askTool(t *testing.T, asker core.Asker, args string, ctx context.Context) <-chan struct {
	result core.ToolResult
	err    error
} {
	t.Helper()
	tool := &tools.AskUserTool{Asker: asker}
	result := make(chan struct {
		result core.ToolResult
		err    error
	}, 1)
	go func() {
		got, err := tool.Execute(ctx, json.RawMessage(args), func(string) {})
		result <- struct {
			result core.ToolResult
			err    error
		}{got, err}
	}()
	return result
}

// rpcQuestionAgentClient drives a real core.Agent through one ask_user_question
// tool call and the follow-up model call. blockSecond makes the continuation
// wait on an explicit channel so queued-turn cancellation tests never rely on
// timing.
type rpcQuestionAgentClient struct {
	blockSecond    bool
	secondQuestion bool

	firstCall      chan struct{}
	secondCall     chan struct{}
	secondRelease  chan struct{}
	secondCanceled chan struct{}

	mu    sync.Mutex
	calls int
}

func newRPCQuestionAgentClient(blockSecond bool) *rpcQuestionAgentClient {
	return &rpcQuestionAgentClient{
		blockSecond:    blockSecond,
		firstCall:      make(chan struct{}),
		secondCall:     make(chan struct{}),
		secondRelease:  make(chan struct{}),
		secondCanceled: make(chan struct{}),
	}
}

func (c *rpcQuestionAgentClient) Name() string { return "rpc-question-fake" }

func installProductionRPCQuestionAgent(t *testing.T, run rpcQuestionRun, client *rpcQuestionAgentClient) (build.LiveToolSet, *rpcAskerBinding) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := build.Args{
		Provider: "openai",
		Model:    "gpt-5",
		CWD:      testsupport.TempDir(t),
		NoExt:    true,
		NoMCP:    true,
	}
	r, err := build.Resolve(args, true)
	if err != nil {
		t.Fatal(err)
	}
	ag := r.NewAgent()
	ag.Client = client
	ag.Model = "fake-model"
	run.server.agent = ag
	run.server.provider = client.Name()
	run.server.model = ag.Model
	binding := &rpcAskerBinding{resolved: &r, agent: ag}
	run.server.bindAsker = binding.bind
	return build.LiveToolSet{Args: args}, binding
}

func assertProductionQuestionBinding(t *testing.T, ag *core.Agent, want core.Asker) {
	t.Helper()
	tool, ok := ag.LookupTool("ask_user_question")
	if !ok {
		t.Fatal("production agent has no ask_user_question tool")
	}
	ask, ok := tool.(*tools.AskUserTool)
	if !ok {
		t.Fatalf("ask_user_question type = %T", tool)
	}
	if ask.Asker != want {
		t.Fatalf("ask_user_question asker = %T, want %T", ask.Asker, want)
	}

	ticketTool, ok := ag.LookupTool("ticket_init")
	if !ok {
		t.Fatal("production agent has no ticket_init tool")
	}
	ticket, ok := ticketTool.(*tools.TicketInitTool)
	if !ok {
		t.Fatalf("ticket_init type = %T", ticketTool)
	}
	if ticket.Asker != want {
		t.Fatalf("ticket_init asker = %T, want %T", ticket.Asker, want)
	}
}

func (c *rpcQuestionAgentClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		switch call {
		case 1:
			close(c.firstCall)
			out <- provider.EventDone{
				Stop: provider.StopToolUse,
				Message: provider.Message{
					Role: provider.RoleAssistant,
					Content: []provider.Content{provider.ToolCallBlock{
						ID:        "ask-call-1",
						Name:      "ask_user_question",
						Arguments: json.RawMessage(`{"question":"Continue?","options":["yes","no"],"allow_custom":false}`),
					}},
				},
			}
		case 2:
			close(c.secondCall)
			if c.secondQuestion {
				out <- provider.EventDone{
					Stop: provider.StopToolUse,
					Message: provider.Message{
						Role: provider.RoleAssistant,
						Content: []provider.Content{provider.ToolCallBlock{
							ID:        "ask-call-2",
							Name:      "ask_user_question",
							Arguments: json.RawMessage(`{"question":"Continue the queued turn?","options":["yes","no"],"allow_custom":false}`),
						}},
					},
				}
				return
			}
			if c.blockSecond {
				select {
				case <-ctx.Done():
					close(c.secondCanceled)
					out <- provider.EventDone{Stop: provider.StopAborted}
					return
				case <-c.secondRelease:
				}
			}
			out <- provider.EventTextDelta{Delta: "continued after the answer"}
			out <- provider.EventDone{
				Stop: provider.StopEnd,
				Message: provider.Message{
					Role:    provider.RoleAssistant,
					Content: []provider.Content{provider.TextBlock{Text: "continued after the answer"}},
				},
			}
		default:
			out <- provider.EventDone{
				Stop: provider.StopEnd,
				Message: provider.Message{
					Role:    provider.RoleAssistant,
					Content: []provider.Content{provider.TextBlock{Text: "unexpected extra turn"}},
				},
			}
		}
	}()
	return out, nil
}

func rpcFrameType(frame map[string]any) string {
	value, _ := frame["type"].(string)
	return value
}

func nextRPCFrameUntil(t *testing.T, out *rpcQuestionFrameWriter, typ string) map[string]any {
	t.Helper()
	for {
		frame := nextRPCQuestionFrame(t, out)
		if rpcFrameType(frame) == typ {
			return frame
		}
	}
}

func nextRPCResponseCommand(t *testing.T, out *rpcQuestionFrameWriter, command string) map[string]any {
	t.Helper()
	for {
		frame := nextRPCQuestionFrame(t, out)
		if rpcFrameType(frame) == "response" && frame["command"] == command {
			return frame
		}
	}
}

func TestRPCStructuredQuestionsCapabilityOffKeepsHeadlessBehavior(t *testing.T) {
	run := startRPCQuestionRun(t, map[string]bool{})
	defer run.close(t)

	tool := &tools.AskUserTool{}
	got, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"continue?"}`), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	details, ok := got.Details.(map[string]any)
	if !ok || details["asked"] != false || details["reason"] != "no_channel" {
		t.Fatalf("headless result = %#v, want no_channel", got.Details)
	}
	if run.server.questionsEnabled() {
		t.Fatal("a hello without structured_questions enabled the question channel")
	}

}

func TestRPCStructuredQuestionsRoundTripPreservesBatchAndResumesSameTool(t *testing.T) {
	run := startRPCQuestionRun(t, map[string]bool{rpcStructuredQuestionsCapability: true})
	defer run.close(t)

	result := askTool(t, run.server, `{"questions":[
		{"question":"Where should it run?","slug":"runtime mode","options":["local","remote"],"allow_custom":false},
		{"question":"Which targets?","slug":"target set","options":["web","mobile","desktop"],"multi_select":true,"allow_custom":true},
		{"question":"What should it be called?","slug":"release name"}
	]}`, context.Background())

	requestID, questions := questionBatch(t, nextRPCQuestionFrame(t, run.out))
	if len(questions) != 3 {
		t.Fatalf("question batch has %d entries, want 3", len(questions))
	}
	if questions[0].ID != requestID+"-0" || questions[1].ID != requestID+"-1" || questions[2].ID != requestID+"-2" {
		t.Fatalf("question ids = %#v, want stable request-position ids", questions)
	}
	if questions[0].Header != "runtime mode" || questions[0].Options[1] != "remote" || questions[0].AllowCustom || questions[0].MultiSelect {
		t.Fatalf("first question lost its native fields: %#v", questions[0])
	}
	if questions[1].Header != "target set" || !questions[1].AllowCustom || !questions[1].MultiSelect {
		t.Fatalf("multi-select question lost its native fields: %#v", questions[1])
	}
	if questions[2].Header != "release name" || len(questions[2].Options) != 0 {
		t.Fatalf("free-form question was not preserved: %#v", questions[2])
	}

	// An incomplete batch is rejected while the original tool remains parked.
	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":      requestID,
		"type":    "question_answer",
		"answers": map[string]any{questions[0].ID: "remote"},
	})
	bad := nextRPCQuestionFrame(t, run.out)
	if bad["type"] != "response" || bad["success"] != false || !strings.Contains(bad["error"].(string), "incomplete") {
		t.Fatalf("incomplete answer response = %#v", bad)
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":   requestID,
		"type": "question_answer",
		"answers": map[string]any{
			questions[0].ID: "remote",
			questions[1].ID: []string{"web", "desktop"},
			questions[2].ID: map[string]any{"answer": "nightly", "note": "keep the name short"},
		},
	})
	resolved := nextRPCQuestionFrame(t, run.out)
	if resolved["type"] != "question_resolved" || resolved["id"] != requestID {
		t.Fatalf("resolution frame = %#v", resolved)
	}
	ack := nextRPCQuestionFrame(t, run.out)
	if ack["type"] != "response" || ack["success"] != true {
		t.Fatalf("answer response = %#v", ack)
	}

	got := <-result
	if got.err != nil {
		t.Fatalf("same tool call did not resume: %v", got.err)
	}
	details, ok := got.result.Details.(map[string]any)
	if !ok {
		t.Fatalf("batch result details = %#v", got.result.Details)
	}
	entries, ok := details["answers"].([]map[string]any)
	if !ok || len(entries) != 3 {
		t.Fatalf("batch result details = %#v", got.result.Details)
	}
	if entries[1]["answer"] != "" || entries[1]["declined"] != false {
		t.Fatalf("multi-select result lost its batch entry: %#v", entries[1])
	}
	if entries[2]["note"] != "keep the name short" {
		t.Fatalf("custom answer note = %#v", entries[2])
	}

	// The first answer consumed the only parked request. A duplicate cannot
	// reach this or another waiting tool call.
	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":      requestID,
		"type":    "question_answer",
		"answers": map[string]any{questions[0].ID: "remote", questions[1].ID: []string{"web"}, questions[2].ID: "again"},
	})
	duplicate := nextRPCQuestionFrame(t, run.out)
	if duplicate["type"] != "response" || duplicate["success"] != false {
		t.Fatalf("duplicate answer response = %#v", duplicate)
	}
}

func TestRPCStructuredQuestionsRejectBadAnswersWithoutCrossUnblocking(t *testing.T) {
	run := startRPCQuestionRun(t, map[string]bool{rpcStructuredQuestionsCapability: true})
	defer run.close(t)

	first := askTool(t, run.server, `{"questions":[{"question":"one?"},{"question":"two?"}]}`, context.Background())
	firstID, firstQuestions := questionBatch(t, nextRPCQuestionFrame(t, run.out))

	sendRPCQuestionLine(t, run.in, `{"id":"broken","type":"question_answer","answers":`)
	malformed := nextRPCQuestionFrame(t, run.out)
	if malformed["type"] != "response" || malformed["success"] != false {
		t.Fatalf("malformed response = %#v", malformed)
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":   firstID,
		"type": "question_answer",
		"answers": map[string]any{
			firstQuestions[0].ID: "one",
			"question-stale-0":   "wrong request",
		},
	})
	mismatched := nextRPCQuestionFrame(t, run.out)
	if mismatched["type"] != "response" || mismatched["success"] != false {
		t.Fatalf("mismatched response = %#v", mismatched)
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":   firstID,
		"type": "question_answer",
		"answers": map[string]any{
			firstQuestions[0].ID: "one",
			firstQuestions[1].ID: "two",
		},
	})
	_ = nextRPCQuestionFrame(t, run.out) // question_resolved
	_ = nextRPCQuestionFrame(t, run.out) // command response
	if got := <-first; got.err != nil {
		t.Fatalf("first question failed after valid answer: %v", got.err)
	}

	second := askTool(t, run.server, `{"question":"new?","options":["yes","no"],"allow_custom":false}`, context.Background())
	secondID, secondQuestions := questionBatch(t, nextRPCQuestionFrame(t, run.out))

	// A stale answer is rejected by the request id before it can touch the
	// newer request, even when its payload contains the newer question id.
	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":   firstID,
		"type": "question_answer",
		"answers": map[string]any{
			secondQuestions[0].ID: "yes",
		},
	})
	stale := nextRPCQuestionFrame(t, run.out)
	if stale["type"] != "response" || stale["success"] != false {
		t.Fatalf("stale response = %#v", stale)
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":      secondID,
		"type":    "question_answer",
		"answers": map[string]any{secondQuestions[0].ID: "yes"},
	})
	_ = nextRPCQuestionFrame(t, run.out)
	_ = nextRPCQuestionFrame(t, run.out)
	if got := <-second; got.err != nil {
		t.Fatalf("new question was affected by stale answer: %v", got.err)
	}
}

func TestRPCStructuredQuestionDismissAbortAndEOFDoNotFabricateAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, run rpcQuestionRun, id string)
	}{
		{
			name: "dismiss",
			end: func(t *testing.T, run rpcQuestionRun, id string) {
				sendRPCQuestionCommand(t, run.in, map[string]any{"id": id, "type": "question_dismiss"})
				if frame := nextRPCQuestionFrame(t, run.out); frame["type"] != "question_dismissed" {
					t.Fatalf("dismiss event = %#v", frame)
				}
				if frame := nextRPCQuestionFrame(t, run.out); frame["success"] != true {
					t.Fatalf("dismiss response = %#v", frame)
				}
			},
		},
		{
			name: "abort",
			end: func(t *testing.T, run rpcQuestionRun, id string) {
				sendRPCQuestionCommand(t, run.in, map[string]any{"id": "abort-1", "type": "abort"})
				if frame := nextRPCQuestionFrame(t, run.out); frame["command"] != "abort" || frame["success"] != true {
					t.Fatalf("abort response = %#v", frame)
				}
			},
		},
		{
			name: "eof",
			end: func(t *testing.T, run rpcQuestionRun, id string) {
				if err := run.in.Close(); err != nil {
					t.Fatal(err)
				}
				if err := <-run.done; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := startRPCQuestionRun(t, map[string]bool{rpcStructuredQuestionsCapability: true})
			questionCtx := context.Background()
			if tc.name == "abort" {
				var cancel context.CancelFunc
				questionCtx, cancel = context.WithCancel(questionCtx)
				run.server.setCancel(cancel)
			}
			result := askTool(t, run.server, `{"question":"continue?"}`, questionCtx)
			id, _ := questionBatch(t, nextRPCQuestionFrame(t, run.out))
			tc.end(t, run, id)
			got := <-result
			if got.err == nil {
				t.Fatalf("%s returned a fabricated answer: %#v", tc.name, got.result)
			}
			if !strings.Contains(got.err.Error(), "ask_user_question") {
				t.Fatalf("%s error = %v, want tool context", tc.name, got.err)
			}
			if tc.name != "eof" {
				run.close(t)
			}
		})
	}
}

func TestRPCStructuredQuestionWaitsUntilExplicitAnswer(t *testing.T) {
	run := startRPCQuestionRun(t, map[string]bool{rpcStructuredQuestionsCapability: true})
	defer run.close(t)

	result := askTool(t, run.server, `{"question":"no expiry?"}`, context.Background())
	id, questions := questionBatch(t, nextRPCQuestionFrame(t, run.out))
	if run.server.questions.Len() != 1 {
		t.Fatalf("parked questions = %d, want one while client is silent", run.server.questions.Len())
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":      id,
		"type":    "question_answer",
		"answers": map[string]any{questions[0].ID: "still waiting"},
	})
	_ = nextRPCQuestionFrame(t, run.out)
	_ = nextRPCQuestionFrame(t, run.out)
	got := <-result
	if got.err != nil {
		t.Fatalf("question expired before explicit answer: %v", got.err)
	}
}

func TestRPCStructuredQuestionProductionLegacyNoCapabilityUsesNoChannel(t *testing.T) {
	run := startRPCQuestionRun(t, nil)
	defer run.close(t)

	client := newRPCQuestionAgentClient(false)
	_, _ = installProductionRPCQuestionAgent(t, run, client)
	assertProductionQuestionBinding(t, run.server.agent, nil)
	if run.server.questionsEnabled() {
		t.Fatal("legacy connection unexpectedly enabled structured questions")
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-1", "type": "prompt", "message": "legacy"})
	started := nextRPCFrameUntil(t, run.out, "response")
	if started["command"] != "prompt" || started["success"] != true {
		t.Fatalf("prompt start response = %#v", started)
	}
	if done := nextRPCFrameUntil(t, run.out, "done"); done["type"] != "done" {
		t.Fatalf("terminal frame = %#v", done)
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 2 {
		t.Fatalf("provider calls = %d, want the no_channel tool result followed by continuation", calls)
	}
}

func TestRPCStructuredQuestionConcurrentStartupBindSurvivesInitialRegistryPublication(t *testing.T) {
	run := startRPCQuestionRun(t, nil)
	defer run.close(t)

	client := newRPCQuestionAgentClient(false)
	live, binding := installProductionRPCQuestionAgent(t, run, client)

	publishEntered := make(chan struct{})
	allowPublish := make(chan struct{})
	binding.beforePublish = func() {
		close(publishEntered)
		<-allowPublish
	}
	bindReady := make(chan struct{})
	allowBind := make(chan struct{})
	binding.beforeBindLock = func() {
		close(bindReady)
		<-allowBind
	}

	rebuildDone := make(chan struct{})
	go func() {
		binding.rebuild(live)
		close(rebuildDone)
	}()
	<-publishEntered

	bindDone := make(chan struct{})
	go func() {
		binding.bind(run.server)
		close(bindDone)
	}()
	<-bindReady
	close(allowBind)

	// rebuild has selected the old nil asker and still owns the mutex while
	// publication is held. The bind goroutine has crossed its launch barrier
	// and is now forced to contend for that same mutex before publication can
	// continue. TryLock makes the held critical section an assertion, rather
	// than an assumption about goroutine scheduling.
	if binding.mu.TryLock() {
		binding.mu.Unlock()
		t.Fatal("initial registry publication did not hold the asker binding mutex")
	}
	close(allowPublish)
	<-rebuildDone
	<-bindDone
	binding.beforePublish = nil
	binding.beforeBindLock = nil

	assertProductionQuestionBinding(t, run.server.agent, run.server)
	negotiateRPCQuestionCapability(t, run, map[string]bool{rpcStructuredQuestionsCapability: true})
	assertProductionQuestionBinding(t, run.server.agent, run.server)

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-1", "type": "prompt", "message": "start"})
	started := nextRPCFrameUntil(t, run.out, "response")
	if started["command"] != "prompt" || started["success"] != true {
		t.Fatalf("prompt start response = %#v", started)
	}
	questionID, questions := questionBatch(t, nextRPCFrameUntil(t, run.out, "question"))
	if len(questions) != 1 || questions[0].Question != "Continue?" {
		t.Fatalf("provider question = %#v", questions)
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":      questionID,
		"type":    "question_answer",
		"answers": map[string]any{questions[0].ID: "yes"},
	})
	resolved := nextRPCFrameUntil(t, run.out, "question_resolved")
	if resolved["id"] != questionID {
		t.Fatalf("resolution id = %#v, want %q", resolved["id"], questionID)
	}
	ack := nextRPCQuestionFrame(t, run.out)
	if ack["type"] != "response" || ack["success"] != true {
		t.Fatalf("answer response = %#v", ack)
	}
	if done := nextRPCFrameUntil(t, run.out, "done"); done["type"] != "done" {
		t.Fatalf("terminal frame = %#v", done)
	}

	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 2 {
		t.Fatalf("provider calls = %d, want initial tool call plus continuation", calls)
	}
	if text := transcriptText(run.server.agent.Messages()); !strings.Contains(text, "continued after the answer") {
		t.Fatalf("continuation was not persisted in transcript: %s", text)
	}
}

func TestRPCStructuredQuestionRealAgentRoundTripContinues(t *testing.T) {
	for _, tc := range []struct {
		name               string
		rebuildBeforeHello bool
	}{
		{name: "rebuild-before-hello", rebuildBeforeHello: true},
		{name: "hello-before-rebuild", rebuildBeforeHello: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := startRPCQuestionRun(t, nil)
			defer run.close(t)

			client := newRPCQuestionAgentClient(false)
			live, binding := installProductionRPCQuestionAgent(t, run, client)
			if tc.rebuildBeforeHello {
				binding.rebuild(live)
				assertProductionQuestionBinding(t, run.server.agent, nil)
			}
			negotiateRPCQuestionCapability(t, run, map[string]bool{rpcStructuredQuestionsCapability: true})
			assertProductionQuestionBinding(t, run.server.agent, run.server)
			if !tc.rebuildBeforeHello {
				binding.rebuild(live)
			}
			binding.rebuild(live)
			assertProductionQuestionBinding(t, run.server.agent, run.server)

			sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-1", "type": "prompt", "message": "start"})
			started := nextRPCFrameUntil(t, run.out, "response")
			if started["command"] != "prompt" || started["success"] != true {
				t.Fatalf("prompt start response = %#v", started)
			}
			questionID, questions := questionBatch(t, nextRPCFrameUntil(t, run.out, "question"))
			if len(questions) != 1 || questions[0].Question != "Continue?" {
				t.Fatalf("provider question = %#v", questions)
			}

			sendRPCQuestionCommand(t, run.in, map[string]any{
				"id":      questionID,
				"type":    "question_answer",
				"answers": map[string]any{questions[0].ID: "yes"},
			})
			resolved := nextRPCFrameUntil(t, run.out, "question_resolved")
			if resolved["id"] != questionID {
				t.Fatalf("resolution id = %#v, want %q", resolved["id"], questionID)
			}
			ack := nextRPCQuestionFrame(t, run.out)
			if ack["type"] != "response" || ack["success"] != true {
				t.Fatalf("answer response = %#v", ack)
			}
			if done := nextRPCFrameUntil(t, run.out, "done"); done["type"] != "done" {
				t.Fatalf("terminal frame = %#v", done)
			}

			client.mu.Lock()
			calls := client.calls
			client.mu.Unlock()
			if calls != 2 {
				t.Fatalf("provider calls = %d, want initial tool call plus continuation", calls)
			}
			messages := run.server.agent.Messages()
			if len(messages) != 4 {
				t.Fatalf("transcript has %d messages, want user, tool call, tool result, continuation", len(messages))
			}
			if messages[0].Role != provider.RoleUser || messages[1].Role != provider.RoleAssistant || messages[2].Role != provider.RoleTool || messages[3].Role != provider.RoleAssistant {
				t.Fatalf("transcript roles = %#v", messages)
			}
			if text := transcriptText(messages); !strings.Contains(text, "continued after the answer") {
				t.Fatalf("continuation was not persisted in transcript: %s", text)
			}
		})
	}
}

func TestRPCStructuredQuestionDismissDoesNotCancelQueuedPrompt(t *testing.T) {
	run := startRPCQuestionRun(t, map[string]bool{rpcStructuredQuestionsCapability: true})
	defer run.close(t)

	client := newRPCQuestionAgentClient(true)
	run.server.agent = core.NewAgent(client, "fake-model", "system", core.Registry{
		"ask_user_question": &tools.AskUserTool{Asker: run.server},
	})
	run.server.provider = client.Name()
	run.server.model = "fake-model"

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-1", "type": "prompt", "message": "first"})
	_ = nextRPCFrameUntil(t, run.out, "response")
	questionID, _ := questionBatch(t, nextRPCFrameUntil(t, run.out, "question"))

	// This prompt waits on turnMu while the first agent turn is parked.
	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-2", "type": "prompt", "message": "queued"})
	sendRPCQuestionCommand(t, run.in, map[string]any{"id": questionID, "type": "question_dismiss"})
	if frame := nextRPCFrameUntil(t, run.out, "question_dismissed"); frame["id"] != questionID {
		t.Fatalf("dismissal frame = %#v", frame)
	}
	if frame := nextRPCQuestionFrame(t, run.out); frame["type"] != "response" || frame["success"] != true {
		t.Fatalf("dismissal response = %#v", frame)
	}

	// The first turn must end before the queued turn can start. Once the fake
	// provider sees call two, its explicit barrier proves the queued context is
	// still alive and was not stolen by dismissal.
	_ = nextRPCFrameUntil(t, run.out, "done")
	select {
	case <-client.secondCall:
	case <-client.secondCanceled:
		t.Fatal("dismissing the first question canceled the queued prompt")
	}
	select {
	case <-client.secondCanceled:
		t.Fatal("queued prompt was canceled before its provider barrier released")
	default:
	}
	close(client.secondRelease)
	_ = nextRPCFrameUntil(t, run.out, "done")
	text := transcriptText(run.server.agent.Messages())
	if !strings.Contains(text, "user:first") || !strings.Contains(text, "user:queued") {
		t.Fatalf("dismissal lost native or queued transcript entries: %s", text)
	}
}

func TestRPCStructuredQuestionAbortLeavesQueuedQuestionAnswerable(t *testing.T) {
	inner := newRPCQuestionFrameWriter()
	barrier := &rpcQuestionBarrierWriter{inner: inner, secondQuestion: make(chan struct{})}
	run := startRPCQuestionBarrierRun(t, barrier)
	defer run.close(t)

	client := newRPCQuestionAgentClient(false)
	client.secondQuestion = true
	live, binding := installProductionRPCQuestionAgent(t, run, client)
	negotiateRPCQuestionCapability(t, run, map[string]bool{rpcStructuredQuestionsCapability: true})
	assertProductionQuestionBinding(t, run.server.agent, run.server)
	binding.rebuild(live)
	assertProductionQuestionBinding(t, run.server.agent, run.server)

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-1", "type": "prompt", "message": "first"})
	_ = nextRPCFrameUntil(t, run.out, "response")
	firstID, _ := questionBatch(t, nextRPCFrameUntil(t, run.out, "question"))
	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-2", "type": "prompt", "message": "queued"})

	oldTurnCancel := run.server.currentTurnCancel()
	if oldTurnCancel == nil {
		t.Fatal("first question did not capture an active turn cancellation")
	}
	run.server.setCancel(func() {
		oldTurnCancel()
		<-barrier.secondQuestion
	})
	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "abort-1", "type": "abort"})

	secondID, secondQuestions := questionBatch(t, nextRPCFrameUntil(t, run.out, "question"))
	if secondID == firstID {
		t.Fatalf("queued prompt reused first question id %q", secondID)
	}
	abort := nextRPCResponseCommand(t, run.out, "abort")
	if abort["success"] != true {
		t.Fatalf("abort response = %#v", abort)
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{
		"id":      secondID,
		"type":    "question_answer",
		"answers": map[string]any{secondQuestions[0].ID: "yes"},
	})
	resolved := nextRPCFrameUntil(t, run.out, "question_resolved")
	if resolved["id"] != secondID {
		t.Fatalf("queued question resolution = %#v, want %q", resolved["id"], secondID)
	}
	ack := nextRPCQuestionFrame(t, run.out)
	if ack["type"] != "response" || ack["success"] != true {
		t.Fatalf("queued answer response = %#v", ack)
	}
	_ = nextRPCFrameUntil(t, run.out, "done")

	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 3 {
		t.Fatalf("provider calls = %d, want aborted first call, queued question, and continuation", calls)
	}
}

func TestRPCStructuredQuestionEOFLeavesLegacyPromptToDrain(t *testing.T) {
	run := startRPCQuestionRun(t, nil)
	defer func() {
		select {
		case <-run.done:
		default:
			_ = run.in.Close()
		}
	}()

	client := &blockingRPCClient{entered: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
	providerClient := blockingProvider{client}
	run.server.agent = core.NewAgent(providerClient, "fake-model", "system", core.Registry{})
	run.server.provider = providerClient.Name()
	run.server.model = "fake-model"

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "prompt-1", "type": "prompt", "message": "legacy"})
	_ = nextRPCFrameUntil(t, run.out, "response")
	<-client.entered
	if err := run.in.Close(); err != nil {
		t.Fatal(err)
	}
	<-run.server.closedSignal()
	select {
	case <-client.canceled:
		t.Fatal("EOF canceled a legacy prompt before its provider result drained")
	default:
	}
	close(client.release)
	if err := <-run.done; err != nil {
		t.Fatalf("legacy prompt run: %v", err)
	}
	select {
	case <-client.canceled:
		t.Fatal("legacy prompt context was canceled during EOF drain")
	default:
	}
}

// blockingProvider makes the EOF test observe context cancellation without a
// timeout. The result is released only after the server has closed its input.
type blockingRPCClient struct {
	entered  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

type blockingProvider struct{ c *blockingRPCClient }

func (p blockingProvider) Name() string { return "blocking" }

func (p blockingProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 3)
	close(p.c.entered)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: p.Name(), Model: req.Model}
		select {
		case <-ctx.Done():
			close(p.c.canceled)
			out <- provider.EventDone{Stop: provider.StopAborted}
		case <-p.c.release:
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "drained"}},
			}}
		}
	}()
	return out, nil
}

type rpcResolutionBarrierWriter struct {
	inner   *rpcQuestionFrameWriter
	seen    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *rpcResolutionBarrierWriter) Write(p []byte) (int, error) {
	var frame map[string]any
	if json.Unmarshal(bytes.TrimSpace(p), &frame) == nil && frame["type"] == "question_resolved" {
		w.once.Do(func() { close(w.seen) })
		<-w.release
	}
	return w.inner.Write(p)
}

func TestRPCStructuredQuestionAnswerOwnsAgainstConnectionClose(t *testing.T) {
	frames := newRPCQuestionFrameWriter()
	writer := &rpcResolutionBarrierWriter{inner: frames, seen: make(chan struct{}), release: make(chan struct{})}
	s := &rpcServer{ctx: context.Background(), out: writer, questionCapability: true}
	result := askTool(t, s, `{"question":"race?"}`, context.Background())
	id, questions := questionBatch(t, nextRPCQuestionFrame(t, frames))

	answerDone := make(chan struct{})
	go func() {
		s.answerQuestion("question_answer", id, []byte(fmt.Sprintf(`{"answers":{"%s":"yes"}}`, questions[0].ID)))
		close(answerDone)
	}()
	<-writer.seen

	closeDone := make(chan struct{})
	go func() {
		s.close()
		close(closeDone)
	}()
	close(writer.release)
	<-answerDone
	<-closeDone

	got := <-result
	if got.err != nil {
		t.Fatalf("answer lost to connection close after it claimed ownership: %v", got.err)
	}
	resolved := nextRPCQuestionFrame(t, frames)
	if resolved["type"] != "question_resolved" {
		t.Fatalf("resolution frame = %#v", resolved)
	}
	ack := nextRPCQuestionFrame(t, frames)
	if ack["type"] != "response" || ack["success"] != true {
		t.Fatalf("answer response = %#v", ack)
	}
}

func TestRPCStructuredQuestionRejectsEveryInvalidAnswerShape(t *testing.T) {
	run := startRPCQuestionRun(t, map[string]bool{rpcStructuredQuestionsCapability: true})
	defer run.close(t)

	result := askTool(t, run.server, `{"questions":[
		{"question":"mode?","options":["one","two"],"allow_custom":false},
		{"question":"features?","options":["a","b"],"multi_select":true,"allow_custom":false}
	]}`, context.Background())
	id, questions := questionBatch(t, nextRPCQuestionFrame(t, run.out))
	if len(questions) != 2 {
		t.Fatalf("question count = %d, want 2", len(questions))
	}

	validAnswers := func() map[string]any {
		return map[string]any{questions[0].ID: "one", questions[1].ID: []string{"a"}}
	}
	cases := []struct {
		name    string
		id      string
		answers map[string]any
		line    string
	}{
		{name: "stale", id: "question-stale", answers: validAnswers()},
		{name: "incomplete", id: id, answers: map[string]any{questions[0].ID: "one"}},
		{name: "mismatched", id: id, answers: map[string]any{questions[0].ID: "one", "question-other-1": []string{"a"}}},
		{name: "wrong type", id: id, answers: map[string]any{questions[0].ID: 7, questions[1].ID: []string{"a"}}},
		{name: "null", id: id, answers: map[string]any{questions[0].ID: nil, questions[1].ID: []string{"a"}}},
		{name: "empty", id: id, answers: map[string]any{questions[0].ID: "", questions[1].ID: []string{"a"}}},
		{name: "closed choice", id: id, answers: map[string]any{questions[0].ID: "three", questions[1].ID: []string{"a"}}},
		{name: "duplicate multi select", id: id, answers: map[string]any{questions[0].ID: "one", questions[1].ID: []string{"a", "a"}}},
		{name: "malformed", id: "broken", line: `{"id":"broken","type":"question_answer","answers":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.line != "" {
				sendRPCQuestionLine(t, run.in, tc.line)
			} else {
				sendRPCQuestionCommand(t, run.in, map[string]any{"id": tc.id, "type": "question_answer", "answers": tc.answers})
			}
			frame := nextRPCQuestionFrame(t, run.out)
			if frame["type"] != "response" || frame["success"] != false {
				t.Fatalf("invalid answer response = %#v", frame)
			}
			if run.server.questions.Len() != 1 {
				t.Fatalf("invalid %s consumed the parked question", tc.name)
			}
		})
	}

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": id, "type": "question_answer", "answers": validAnswers()})
	_ = nextRPCQuestionFrame(t, run.out)
	_ = nextRPCQuestionFrame(t, run.out)
	if got := <-result; got.err != nil {
		t.Fatalf("valid answer failed after invalid answers: %v", got.err)
	}
}

func TestRPCStructuredQuestionParentCancellationAbortsRealAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := newRPCQuestionFrameWriter()
	server := &rpcServer{ctx: ctx, out: out, questionCapability: true}
	client := newRPCQuestionAgentClient(false)
	server.agent = core.NewAgent(client, "fake-model", "system", core.Registry{
		"ask_user_question": &tools.AskUserTool{Asker: server},
	})
	server.provider = client.Name()
	server.model = "fake-model"

	done := make(chan struct{})
	go func() {
		server.runPrompt("prompt-1", "parent cancel", nil)
		close(done)
	}()
	_ = nextRPCFrameUntil(t, out, "response")
	_ = nextRPCFrameUntil(t, out, "question")
	cancel()
	<-done

	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 1 {
		t.Fatalf("parent cancellation allowed provider continuation call: %d calls", calls)
	}
	if frame := nextRPCFrameUntil(t, out, "done"); frame["type"] != "done" {
		t.Fatalf("terminal frame = %#v", frame)
	}
}

func TestRPCStructuredQuestionAuthAndNegotiation(t *testing.T) {
	t.Setenv("TERVACORE_RPC_TOKEN", "rpc-test-token")
	reader, writer := io.Pipe()
	out := newRPCQuestionFrameWriter()
	server := &rpcServer{ctx: context.Background(), out: out}
	done := make(chan error, 1)
	go func() { done <- server.run(reader) }()

	sendRPCQuestionCommand(t, writer, map[string]any{"id": "before-hello", "type": "prompt", "message": "no"})
	before := nextRPCQuestionFrame(t, out)
	if before["success"] != false || !strings.Contains(before["error"].(string), "auth required") {
		t.Fatalf("pre-auth command = %#v", before)
	}
	sendRPCQuestionCommand(t, writer, map[string]any{
		"id": "hello-1", "type": "hello", "token": "rpc-test-token",
		"capabilities": map[string]bool{rpcStructuredQuestionsCapability: true},
	})
	hello := nextRPCQuestionFrame(t, out)
	if hello["type"] != "response" || hello["success"] != true || !server.questionsEnabled() {
		t.Fatalf("authenticated capability negotiation = %#v", hello)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("auth test server: %v", err)
	}
}
