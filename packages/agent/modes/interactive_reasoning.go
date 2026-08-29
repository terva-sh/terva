package modes

import (
	"context"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// openReasoningDialog shows the thinking-depth picker for this session, seeded
// with the session's current override and the global default so the "inherit"
// row can say what inheriting would actually mean.
func (i *Interactive) openReasoningDialog() {
	if i.cfg.Carrier == nil {
		return
	}
	model, _ := provider.FindModel(i.cfg.Provider, i.cfg.Model)
	i.reasoningDialog.Open(i.sessionReasoning(), i.cfg.Reasoning, model)
	i.invalidate()
}

// sessionReasoning reads this session's override from the carrier — the
// authoritative copy — rather than a local mirror, for the reason swapModel
// re-reads the session after a switch instead of trusting what it just sent.
func (i *Interactive) sessionReasoning() string {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if c == nil || sess == "" {
		return ""
	}
	info, err := c.ResumeSession(context.Background(), sess)
	if err != nil {
		return ""
	}
	return info.Reasoning
}

// applyReasoningSelection sends a level for THIS session. Empty (or "inherit")
// clears the override and puts the session back under the global setting.
func (i *Interactive) applyReasoningSelection(level string) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if c == nil {
		return
	}
	if err := c.SetSessionReasoning(context.Background(), sess, level); err != nil {
		i.setStatusErr(err.Error())
		i.invalidate()
		return
	}
	// Read back rather than echoing what was sent: the daemon normalizes
	// aliases ("hi" becomes "high"), and reporting the sent spelling would tell
	// the user their session is at a level that is not what the record says.
	applied := i.sessionReasoning()
	i.mu.Lock()
	// Update the status bar's cache here too. noteSessionMeta would catch this
	// on the next snapshot, but "next snapshot" can be a whole turn away, and
	// the bar disagreeing with the change the user just made is the bug.
	i.carrierReasoning = applied
	if applied == "" {
		// Same chain the turn will walk, from the one symbol that owns it.
		// Testing the global first and falling back to the model's default
		// puts an OPERATOR's per-model level below the global, which is the
		// wrong way round — see provider.ResolveReasoning.
		model, _ := provider.FindModel(i.cfg.Provider, i.cfg.Model)
		switch lv, from := provider.ResolveReasoning("", model, i.cfg.Reasoning); from {
		case provider.ReasoningFromModelOperator:
			i.statusOK = i18n.T("thinking: following this model's configured level (%s)", lv)
		case provider.ReasoningFromGlobal:
			i.statusOK = i18n.T("thinking: following the global setting (%s)", lv)
		case provider.ReasoningFromModelCatalog:
			i.statusOK = i18n.T("thinking: following the model's default (%s)", lv)
		default:
			i.statusOK = i18n.T("thinking: following the model's default")
		}
	} else {
		i.statusOK = i18n.T("thinking for this session: %s", applied)
	}
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// effectiveReasoning is the level the NEXT turn will actually run at, for the
// status pane: this session's override when it has one, the global otherwise.
//
// The pane read i.cfg.Reasoning directly until sessions could override it, at
// which point that value became the wrong answer for exactly the sessions
// someone had gone out of their way to change — the ones most likely to be
// asking. An override is tagged so the two are distinguishable at a glance.
func (i *Interactive) effectiveReasoning() string {
	if over := i.sessionReasoning(); over != "" {
		return i18n.T("%s (this session)", over)
	}
	return i.cfg.Reasoning
}
