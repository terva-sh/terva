package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"terva.sh/terva/packages/core"
)

const rpcStructuredQuestionsCapability = "structured_questions"

var (
	errRPCQuestionsDisabled = errors.New("structured questions are not enabled for this rpc connection")
	errRPCQuestionClosed    = errors.New("rpc connection closed while waiting for a question")
)

type rpcQuestionState uint8

const (
	rpcQuestionPending rpcQuestionState = iota
	rpcQuestionResolving
	rpcQuestionCancelled
)

type rpcPendingQuestion struct {
	questions      []core.UserQuestion
	cancel         context.CancelFunc
	turnCancel     context.CancelFunc
	state          rpcQuestionState
	resolvedFrames chan struct{}
}

// helloData advertises features supported by this server. A client must still
// opt in through hello before the question channel is bound to the tools.
func (s *rpcServer) helloData() map[string]any {
	return map[string]any{
		"protocol_version": 1,
		"version":          s.version,
		"provider":         s.provider,
		"model":            s.model,
		"capabilities": map[string]bool{
			rpcStructuredQuestionsCapability: true,
		},
	}
}

func (s *rpcServer) negotiateCapabilities(capabilities map[string]bool) {
	s.capMu.Lock()
	if s.capabilityNegotiated {
		s.capMu.Unlock()
		return
	}
	s.capabilityNegotiated = true
	if s.turnStarted {
		s.capMu.Unlock()
		return
	}
	enabled := capabilities != nil && capabilities[rpcStructuredQuestionsCapability]
	s.questionCapability = enabled
	bind := s.bindAsker
	s.capMu.Unlock()

	if enabled && bind != nil {
		bind(s)
	}
}

func (s *rpcServer) markTurnStarted() {
	s.capMu.Lock()
	s.turnStarted = true
	s.capMu.Unlock()
}

func (s *rpcServer) questionsEnabled() bool {
	s.capMu.Lock()
	defer s.capMu.Unlock()
	return s.questionCapability
}

// closedSignal lazily initializes the lifetime channel for small test fixtures
// that construct rpcServer directly instead of going through runRPCMode.
func (s *rpcServer) closedSignal() <-chan struct{} {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed == nil {
		s.closed = make(chan struct{})
	}
	return s.closed
}

// close ends the RPC lifetime before run waits for in-flight commands. It
// cancels only turns that are waiting on a negotiated structured question.
// Legacy piped prompts must still drain their provider result after stdin EOF.
func (s *rpcServer) close() {
	s.lifecycleMu.Lock()
	if s.closed == nil {
		s.closed = make(chan struct{})
	}
	select {
	case <-s.closed:
		s.lifecycleMu.Unlock()
	default:
		close(s.closed)
		s.lifecycleMu.Unlock()
	}

	s.cancelPendingQuestions()
}

// Ask implements core.Asker for a negotiated RPC connection. The request id
// identifies the whole batch. Each question id is derived from it and its
// stable position, so a client can correlate answers without using text.
func (s *rpcServer) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	if !s.questionsEnabled() {
		return nil, errRPCQuestionsDisabled
	}
	if len(qs) == 0 {
		return nil, errors.New("structured question batch is empty")
	}

	waitCtx, cancel := context.WithCancel(ctx)
	turnCancel := s.currentTurnCancel()
	s.questionMu.Lock()
	s.questionSeq++
	requestID := "question-" + strconv.Itoa(s.questionSeq)
	ch, release, ok := s.questions.Park(requestID)
	if !ok {
		s.questionMu.Unlock()
		cancel()
		return nil, fmt.Errorf("structured question id collision: %s", requestID)
	}
	if s.pendingQuestions == nil {
		s.pendingQuestions = make(map[string]*rpcPendingQuestion)
	}
	copied := cloneQuestions(qs)
	pending := &rpcPendingQuestion{
		questions:      copied,
		cancel:         cancel,
		turnCancel:     turnCancel,
		state:          rpcQuestionPending,
		resolvedFrames: make(chan struct{}),
	}
	s.pendingQuestions[requestID] = pending
	s.questionMu.Unlock()
	defer release()
	defer s.removePendingQuestion(requestID)
	defer cancel()

	s.writeEvent(map[string]any{
		"type":      "question",
		"id":        requestID,
		"questions": wireQuestions(requestID, copied),
	})

	select {
	case answers := <-ch:
		// answerQuestion closes this after it has emitted question_resolved and
		// the command response. The waiting tool therefore cannot emit its
		// result before those frames, even though ParkTable delivers eagerly.
		<-pending.resolvedFrames
		return answers, nil
	case <-waitCtx.Done():
		if s.cancelQuestion(requestID) {
			cancelOwnedQuestion(pending)
			return nil, waitCtx.Err()
		}
		if s.questionResolving(requestID) {
			answers := <-ch
			<-pending.resolvedFrames
			return answers, nil
		}
		return nil, waitCtx.Err()
	case <-s.ctx.Done():
		if s.cancelQuestion(requestID) {
			cancelOwnedQuestion(pending)
			return nil, s.ctx.Err()
		}
		if s.questionResolving(requestID) {
			answers := <-ch
			<-pending.resolvedFrames
			return answers, nil
		}
		return nil, s.ctx.Err()
	case <-s.closedSignal():
		if s.cancelQuestion(requestID) {
			cancelOwnedQuestion(pending)
			return nil, errRPCQuestionClosed
		}
		if s.questionResolving(requestID) {
			answers := <-ch
			<-pending.resolvedFrames
			return answers, nil
		}
		return nil, errRPCQuestionClosed
	}
}

func cloneQuestions(qs []core.UserQuestion) []core.UserQuestion {
	out := make([]core.UserQuestion, len(qs))
	for i, q := range qs {
		out[i] = q
		out[i].Options = append([]string(nil), q.Options...)
	}
	return out
}

func wireQuestions(requestID string, qs []core.UserQuestion) []map[string]any {
	out := make([]map[string]any, len(qs))
	for i, q := range qs {
		header := q.Slug
		if header == "" {
			header = fmt.Sprintf("Question %d", i+1)
		}
		out[i] = map[string]any{
			"id":           requestID + "-" + strconv.Itoa(i),
			"header":       header,
			"question":     q.Question,
			"options":      q.Options,
			"allow_custom": q.AllowCustom,
			"multi_select": q.MultiSelect,
		}
	}
	return out
}

func (s *rpcServer) removePendingQuestion(id string) {
	s.questionMu.Lock()
	delete(s.pendingQuestions, id)
	s.questionMu.Unlock()
}

// cancelQuestion claims cancellation before invoking either callback. An
// answer command can therefore either own the request or lose it, but cannot
// emit a success after dismissal or connection closure has taken ownership.
func (s *rpcServer) cancelQuestion(id string) bool {
	s.questionMu.Lock()
	defer s.questionMu.Unlock()
	pending, ok := s.pendingQuestions[id]
	if !ok || pending.state != rpcQuestionPending {
		return false
	}
	pending.state = rpcQuestionCancelled
	return true
}

func (s *rpcServer) questionResolving(id string) bool {
	s.questionMu.Lock()
	defer s.questionMu.Unlock()
	pending, ok := s.pendingQuestions[id]
	return ok && pending.state == rpcQuestionResolving
}

func cancelOwnedQuestion(pending *rpcPendingQuestion) {
	if pending.cancel != nil {
		pending.cancel()
	}
	if pending.turnCancel != nil {
		pending.turnCancel()
	}
}

func (s *rpcServer) cancelPendingQuestions() {
	s.questionMu.Lock()
	pending := make([]*rpcPendingQuestion, 0, len(s.pendingQuestions))
	for _, question := range s.pendingQuestions {
		if question.state != rpcQuestionPending {
			continue
		}
		question.state = rpcQuestionCancelled
		pending = append(pending, question)
	}
	s.questionMu.Unlock()
	for _, question := range pending {
		cancelOwnedQuestion(question)
	}
}

func (s *rpcServer) answerQuestion(cmd, id string, raw []byte) {
	if id == "" {
		s.writeError(id, cmd, "question answer requires the question request id")
		return
	}
	var req struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeError(id, cmd, "malformed question answer: "+err.Error())
		return
	}
	if req.Answers == nil {
		s.writeError(id, cmd, "question answer requires an answers object")
		return
	}

	s.questionMu.Lock()
	pending, ok := s.pendingQuestions[id]
	if !ok {
		s.questionMu.Unlock()
		s.writeError(id, cmd, "no pending structured question with id "+id)
		return
	}
	if pending.state != rpcQuestionPending {
		s.questionMu.Unlock()
		s.writeError(id, cmd, "structured question already has an answer")
		return
	}
	answers, wireAnswers, err := decodeQuestionAnswers(id+"-", pending.questions, req.Answers)
	if err != nil {
		s.questionMu.Unlock()
		s.writeError(id, cmd, err.Error())
		return
	}
	pending.state = rpcQuestionResolving

	// Keep the ownership lock through both frames and the ParkTable delivery.
	// Cancellation cannot take the request after the answer has claimed it, and
	// the waiting tool cannot resume until the response is on the wire.
	s.writeEvent(map[string]any{
		"type":    "question_resolved",
		"id":      id,
		"answers": wireAnswers,
	})
	if !s.questions.Deliver(id, answers) {
		// ParkTable is owned by Ask and cannot lose this waiter after the state
		// claim above. Keep the failure path honest if that invariant changes.
		s.writeError(id, cmd, "structured question delivery failed")
		close(pending.resolvedFrames)
		s.questionMu.Unlock()
		return
	}
	s.writeResponse(id, cmd, map[string]any{"resolved": true})
	close(pending.resolvedFrames)
	s.questionMu.Unlock()
}

func (s *rpcServer) dismissQuestion(id string) {
	if id == "" {
		s.writeError(id, "question_dismiss", "question dismissal requires the question request id")
		return
	}
	s.questionMu.Lock()
	pending, ok := s.pendingQuestions[id]
	if !ok || pending.state != rpcQuestionPending {
		s.questionMu.Unlock()
		s.writeError(id, "question_dismiss", "no pending structured question with id "+id)
		return
	}
	pending.state = rpcQuestionCancelled
	s.questionMu.Unlock()

	s.writeEvent(map[string]any{"type": "question_dismissed", "id": id})
	s.writeResponse(id, "question_dismiss", map[string]any{"dismissed": true})
	cancelOwnedQuestion(pending)
}

func decodeQuestionAnswers(prefix string, qs []core.UserQuestion, raw map[string]json.RawMessage) ([]core.UserAnswer, map[string]any, error) {
	if len(raw) != len(qs) {
		return nil, nil, fmt.Errorf("incomplete or mismatched question answers: got %d answers for %d questions", len(raw), len(qs))
	}
	return decodeQuestionAnswersWithPrefix(qs, raw, prefix)
}

func decodeQuestionAnswersWithPrefix(qs []core.UserQuestion, raw map[string]json.RawMessage, prefix string) ([]core.UserAnswer, map[string]any, error) {
	answers := make([]core.UserAnswer, len(qs))
	wire := make(map[string]any, len(qs))
	for i, q := range qs {
		id := prefix + strconv.Itoa(i)
		value, ok := raw[id]
		if !ok {
			return nil, nil, fmt.Errorf("incomplete or mismatched question answers: missing %s", id)
		}
		answer, normalized, err := decodeOneQuestionAnswer(q, value)
		if err != nil {
			return nil, nil, fmt.Errorf("question %s: %w", id, err)
		}
		answers[i] = answer
		wire[id] = normalized
	}
	for id := range raw {
		found := false
		for i := range qs {
			if id == prefix+strconv.Itoa(i) {
				found = true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("mismatched question answer id %s", id)
		}
	}
	return answers, wire, nil
}

func decodeOneQuestionAnswer(q core.UserQuestion, raw json.RawMessage) (core.UserAnswer, any, error) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return core.UserAnswer{}, nil, errors.New("answer is required")
	}

	var answer core.UserAnswer
	var normalized any
	switch value[0] {
	case '"':
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return answer, nil, errors.New("answer must be a string or an array of strings")
		}
		answer.Answer = text
		if q.MultiSelect {
			answer.Answers = []string{text}
		}
		normalized = text
	case '[':
		var choices []string
		if err := json.Unmarshal(value, &choices); err != nil {
			return answer, nil, errors.New("answers must be an array of strings")
		}
		if !q.MultiSelect && len(choices) != 1 {
			return answer, nil, errors.New("single-select questions need exactly one answer")
		}
		answer.Answers = choices
		if !q.MultiSelect {
			answer.Answer = choices[0]
		}
		normalized = choices
	case '{':
		var object struct {
			Answer   *string   `json:"answer"`
			Answers  *[]string `json:"answers"`
			Note     string    `json:"note"`
			Declined bool      `json:"declined"`
		}
		if err := json.Unmarshal(value, &object); err != nil {
			return answer, nil, errors.New("answer object is malformed")
		}
		if object.Declined {
			return answer, nil, errors.New("use question_dismiss instead of fabricating a declined answer")
		}
		switch {
		case object.Answer != nil && object.Answers != nil:
			return answer, nil, errors.New("answer object must contain answer or answers, not both")
		case object.Answer != nil:
			answer.Answer = *object.Answer
			if q.MultiSelect {
				answer.Answers = []string{answer.Answer}
			}
		case object.Answers != nil:
			answer.Answers = append([]string(nil), (*object.Answers)...)
			if !q.MultiSelect && len(answer.Answers) != 1 {
				return answer, nil, errors.New("single-select questions need exactly one answer")
			}
			if !q.MultiSelect {
				answer.Answer = answer.Answers[0]
			}
		default:
			return answer, nil, errors.New("answer object must contain answer or answers")
		}
		answer.Note = object.Note
		if q.MultiSelect {
			normalized = map[string]any{"answers": answer.Chosen()}
		} else {
			normalized = map[string]any{"answer": answer.Answer}
		}
		if answer.Note != "" {
			normalized.(map[string]any)["note"] = answer.Note
		}
	default:
		return answer, nil, errors.New("answer must be a string, an array of strings, or an answer object")
	}

	if q.MultiSelect && len(answer.Answers) == 0 && len(q.Options) == 0 {
		return answer, nil, errors.New("a free-form question needs an answer")
	}
	if !q.MultiSelect && strings.TrimSpace(answer.Answer) == "" {
		return answer, nil, errors.New("answer cannot be empty")
	}
	if q.MultiSelect {
		for _, choice := range answer.Answers {
			if strings.TrimSpace(choice) == "" {
				return answer, nil, errors.New("answers cannot contain an empty choice")
			}
		}
	}

	if len(q.Options) > 0 && !q.AllowCustom {
		allowed := make(map[string]struct{}, len(q.Options))
		for _, option := range q.Options {
			allowed[option] = struct{}{}
		}
		for _, choice := range answer.Chosen() {
			if _, ok := allowed[choice]; !ok {
				return answer, nil, fmt.Errorf("%q is not an option and custom answers are disabled", choice)
			}
		}
	}
	if q.MultiSelect {
		seen := make(map[string]struct{}, len(answer.Answers))
		for _, choice := range answer.Answers {
			if _, ok := seen[choice]; ok {
				return answer, nil, fmt.Errorf("answer %q is repeated", choice)
			}
			seen[choice] = struct{}{}
		}
	}
	return answer, normalized, nil
}

// The RPC server is the negotiated implementation of core.Asker.
var _ core.Asker = (*rpcServer)(nil)
