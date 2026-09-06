package core

import (
	"errors"
	"fmt"

	"terva.sh/terva/packages/i18n"
)

// ErrPersistence identifies a durable-session failure. The live transcript
// remains available, but this agent refuses further turns through that handle.
var ErrPersistence = errors.New("session persistence failed")

// RecordPersistenceError latches the first failure from a persistence observer.
// Observers run outside the agent lock. Runs return the failure at their next
// safe boundary; callers can also query PersistenceError while the agent is idle.
func (a *Agent) RecordPersistenceError(err error) {
	if a == nil || err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.persistenceErr == nil {
		a.persistenceErr = fmt.Errorf("%w: %s: %w", ErrPersistence,
			i18n.T("History could not be saved. Preserve the live transcript, fix storage, then reopen the saved session"), err)
	}
}

// PersistenceError returns the first persistence failure, or nil. There is no
// reset: a failed row may have partly reached disk, so retrying could duplicate it.
func (a *Agent) PersistenceError() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.persistenceErr
}

func (a *Agent) withPersistenceError(err error) error {
	perr := a.PersistenceError()
	if perr == nil || errors.Is(err, ErrPersistence) {
		return err
	}
	if err == nil {
		return perr
	}
	return errors.Join(err, perr)
}
