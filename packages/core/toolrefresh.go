package core

// The tool-refresh seam: a tool that changes what the workspace CAN offer asks
// its host to re-resolve the tool set, so the new tools land on the next turn
// instead of the next session.
//
// ticket_init is the first caller. A repository with no .tickets store
// registers none of the ten ticket_* tools, so an agent that provisions the
// store mid-session would otherwise sit there unable to use the thing it just
// created, and the user would have to restart terva to see it.
//
// This is deliberately NOT ActivateGroup. Activation changes which registered
// tools are ADVERTISED. A refresh re-runs registration itself, because the gate
// that decides membership (a store on disk, an extension that loaded, a trust
// flip) has moved. The two compose: a group the model already activated stays
// active across the rebuild, so the ten ticket tools arrive advertised.
//
// The callback lives on the agent, which outlives every rebuild. A tool
// instance does not: a rebuild mints fresh tools, and a channel bound to the
// instance is nil from then on. That failure has shipped three times already,
// once per channel, and workspace_toolchannels.go carries the account.

// SetToolRefresher installs the host's re-resolve callback. Hosts call it once,
// when the session is built. A second call replaces the first.
//
// nil clears it, and a host that never calls it leaves refresh unavailable:
// RequestToolRefresh then reports false and the caller tells the model its new
// tools arrive in the next session. That is the honest answer for a one-shot
// print or cli run, which has no rebuild to fire.
func (a *Agent) SetToolRefresher(fn func(reason string)) {
	a.obsMu.Lock()
	a.toolRefresh = fn
	a.obsMu.Unlock()
}

// ToolRefreshAvailable reports whether this session can re-resolve its tools.
// A tool reads it BEFORE it acts, so its result can promise the right thing.
func (a *Agent) ToolRefreshAvailable() bool {
	if a == nil {
		return false
	}
	a.obsMu.RLock()
	defer a.obsMu.RUnlock()
	return a.toolRefresh != nil
}

// RequestToolRefresh asks the host to re-resolve the tool set and reports
// whether a host was there to ask. reason labels the trigger for the host's
// prompt-rebuilt notice, so the user reads why their prompt cache moved.
//
// The refresh runs synchronously and outside a.mu, because a host callback that
// reaches back into the agent under the agent lock deadlocks. It lands at the
// next turn's pin like every other tool-set change, never mid-turn.
func (a *Agent) RequestToolRefresh(reason string) bool {
	if a == nil {
		return false
	}
	a.obsMu.RLock()
	fn := a.toolRefresh
	a.obsMu.RUnlock()
	if fn == nil {
		return false
	}
	fn(reason)
	return true
}
