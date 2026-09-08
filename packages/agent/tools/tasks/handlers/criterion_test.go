package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/tasks"
)

// recordingChecker stands in for the ticket store, so these tests pin when the
// handler reaches for it without needing a ticket store to exist.
type recordingChecker struct {
	calls []string
	err   error
}

func (c *recordingChecker) CheckCriterion(ref string, index int) error {
	c.calls = append(c.calls, fmt.Sprintf("%s#%d", ref, index))
	return c.err
}

// seededTask makes a task that carries a ticket linkage, the way a claim does.
func seededTask(t *testing.T, s *tasks.Store, ref string, criterion int) tasks.Task {
	t.Helper()
	made, err := s.Create([]tasks.CreateSpec{{
		Title:     "seeded work",
		Ticket:    ref,
		Criterion: criterion,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return made[0]
}

func closeWith(t *testing.T, s *tasks.Store, id, status, evidence string) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": id, "status": status, "evidence": evidence})
	return Update(s, raw)
}

func TestCloseWithEvidenceChecksTheCriterion(t *testing.T) {
	s := boundStore(t)
	ck := &recordingChecker{}
	s.SetCriterionChecker(ck)
	task := seededTask(t, s, "TKT-ABC", 3)

	text, isErr := closeWith(t, s, task.ID, "done", "go test ./... passed")
	if isErr {
		t.Fatalf("close failed: %s", text)
	}
	if len(ck.calls) != 1 || ck.calls[0] != "TKT-ABC#3" {
		t.Fatalf("checker calls = %v, want one call for TKT-ABC#3", ck.calls)
	}
	if !strings.Contains(text, "Checked acceptance criterion 3 on TKT-ABC") {
		t.Errorf("the result does not report the check: %s", text)
	}
}

func TestCloseWithoutEvidenceChecksNothing(t *testing.T) {
	s := boundStore(t)
	ck := &recordingChecker{}
	s.SetCriterionChecker(ck)
	task := seededTask(t, s, "TKT-ABC", 1)

	if _, isErr := closeWith(t, s, task.ID, "done", ""); isErr {
		t.Fatal("an unevidenced close should still close the task")
	}
	if len(ck.calls) != 0 {
		t.Errorf("an unevidenced close ticked a criterion: %v", ck.calls)
	}
}

// Blocked is a park, not a completion. It carries evidence for the same reason
// done does, but nothing about it says the criterion is satisfied.
func TestBlockingATaskChecksNothing(t *testing.T) {
	s := boundStore(t)
	ck := &recordingChecker{}
	s.SetCriterionChecker(ck)
	task := seededTask(t, s, "TKT-ABC", 1)

	if _, isErr := closeWith(t, s, task.ID, "blocked", "waiting on review"); isErr {
		t.Fatal("blocking the task failed")
	}
	if len(ck.calls) != 0 {
		t.Errorf("blocking a task ticked a criterion: %v", ck.calls)
	}
}

// A session with no ticket store binds no checker. That is the normal case for
// most repositories, and it must not turn a close into an error.
func TestCloseWithNoCheckerBoundStillCloses(t *testing.T) {
	s := boundStore(t)
	task := seededTask(t, s, "TKT-ABC", 1)

	text, isErr := closeWith(t, s, task.ID, "done", "verified")
	if isErr {
		t.Fatalf("close failed with no checker bound: %s", text)
	}
	if strings.Contains(text, "acceptance criterion") {
		t.Errorf("a session with no ticket store mentioned a criterion: %s", text)
	}
}

// The ticket store can refuse: a stale revision, a missing ticket, a criterion
// index the ticket no longer has. None of that unmakes the task close, and
// reporting the close as an error would invite the model to close it again.
func TestAFailedCriterionCheckDoesNotFailTheClose(t *testing.T) {
	s := boundStore(t)
	s.SetCriterionChecker(&recordingChecker{err: errors.New("stale_revision")})
	task := seededTask(t, s, "TKT-ABC", 2)

	text, isErr := closeWith(t, s, task.ID, "done", "go test ./... passed")
	if isErr {
		t.Fatalf("a failed criterion check failed the close: %s", text)
	}
	if !strings.Contains(text, "stale_revision") {
		t.Errorf("the result hides why the criterion stayed unchecked: %s", text)
	}
	if !strings.Contains(text, "git ticket ac TKT-ABC --check 2") {
		t.Errorf("the result does not name the manual remedy: %s", text)
	}
	// The task itself still closed.
	for _, got := range s.List() {
		if got.ID == task.ID && got.Status != tasks.StatusDone {
			t.Errorf("the task is %s, want done", got.Status)
		}
	}
}
