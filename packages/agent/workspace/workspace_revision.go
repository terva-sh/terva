package workspace

import (
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

// revise serializes admission, durable writes, live changes, and variant state.
// Lock order is revisionMu, then mu or the agent/session locks. No caller may
// acquire revisionMu while holding mu. Publish only after releasing revisionMu
// so a subscriber can reenter the session.
func (s *wsSession) revise(epoch *uint64, change func() error) error {
	s.revisionMu.Lock()
	err := s.revisionGuard(epoch)
	if err == nil {
		err = change()
	}
	s.revisionMu.Unlock()
	if err == nil {
		s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	}
	return err
}

func (s *wsSession) editMessage(epoch uint64, index int, text string) error {
	return s.revise(&epoch, func() error { return s.editMessageHeld(index, text) })
}

func (s *wsSession) deleteMessage(epoch uint64, index int) error {
	return s.revise(&epoch, func() error { return s.deleteMessageHeld(index) })
}

func (s *wsSession) swipe(epoch uint64, variant int) error {
	return s.revise(&epoch, func() error { return s.swipeHeld(variant) })
}

func (s *wsSession) swipeMessage(epoch uint64, index, variant int) error {
	return s.revise(&epoch, func() error { return s.swipeMessageHeld(index, variant) })
}

func (s *wsSession) pruneVariants(epoch uint64, index int) error {
	return s.revise(&epoch, func() error { return s.pruneVariantsHeld(index) })
}

func (s *wsSession) dropVariant(epoch uint64, index, variant int) error {
	return s.revise(&epoch, func() error { return s.dropVariantHeld(index, variant) })
}

func (s *wsSession) clear() error {
	if err := s.revise(nil, s.clearHeld); err != nil {
		return err
	}
	s.broadcast(ctrlproto.NoticeEvent("info", "", i18n.T("Cleared the conversation.")))
	return nil
}

func (s *wsSession) postDirected(actor, text string) error {
	return s.revise(nil, func() error { return s.postDirectedHeld(actor, text) })
}

// persistenceFailure records a failed write without calling subscribers under
// the revision lock. The command or turn reports the error after release.
func (s *wsSession) persistenceFailure(err error) error {
	s.agent.RecordPersistenceError(err)
	if err := s.agent.PersistenceError(); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "%v", err)
	}
	return nil
}
