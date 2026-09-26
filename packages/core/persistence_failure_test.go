package core

import (
	"context"
	"errors"
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestPersistenceFailureStopsBeforeToolExecution(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
		return calledATool(10), nil
	}}
	tool := &recordingTool{}
	ag := newTestAgent(client, "test", "", Registry{"echo": tool})
	ag.maxSteps = 1
	ag.AddMessageObserver(func(m provider.Message) {
		if m.Role == provider.RoleAssistant {
			ag.RecordPersistenceError(errSessionStorage)
		}
	})
	err := ag.Prompt(context.Background(), "run the tool", nil, nil)
	if !errors.Is(err, ErrPersistence) || !errors.Is(err, errSessionStorage) {
		t.Fatalf("Prompt returned %v; want the persistence failure", err)
	}
	if tool.lastArgs != nil {
		t.Fatal("tool ran after its call could not be persisted")
	}
}

var errSessionStorage = errors.New("synthetic storage failure")
