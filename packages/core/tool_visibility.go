package core

// ToolVisibility decides which registered tools a request ADVERTISES to the
// model. It filters only the tool specs sent in the request (SpecsVisible); it
// never touches dispatch or the permission gate, both of which resolve the
// full registry, so a tool it hides stays callable and stays gated.
// Advertisement is not authority.
//
// The engine pins the answer once per segment of the loop, so a change lands
// at the next pin and never between the steps of one segment. Growth can
// re-pin within a Prompt, if BeginPrompt asks for that: after the tool batch
// that grew the set, and at a segment boundary. packages/core/lazytools is the
// implementation terva attaches.
type ToolVisibility interface {
	// Advertise returns whether each tool in reg is advertised. pin is true
	// when the engine pins a segment's advertisement, and false when it only
	// reads the prefix (the prefix-change guard, the compaction prefix). It may
	// be called with the agent's lock held, so it must not call the agent.
	//
	// The predicate must be pure in the name, so that the advertised set, and
	// with it the cached prompt prefix, is stable while nothing changes. A name
	// absent from reg must be advertised. A nil predicate advertises every tool.
	Advertise(reg Registry, pin bool) func(name string) bool
	// Grew reports whether reg advertises a tool now that the last pin did
	// not. The engine asks it against the pinned registry after a tool batch,
	// and against the live registry at a segment boundary.
	Grew(reg Registry) bool
	// BeginPrompt is called once as a Prompt's loop starts, before the first
	// pin. It reports whether a grown advertisement re-pins within this
	// Prompt. False leaves the growth to the next Prompt's pin.
	BeginPrompt() (repin bool)
}

// GroupRestorer is a ToolVisibility that restores the tool groups a session
// activated. Resume hands it Transcript.ActiveToolGroups.
type GroupRestorer interface {
	RestoreActiveGroups(groups []string)
}

// setToolVisibility sets the visibility the engine consults at each pin. nil
// advertises the whole registry, which is the default. The next pin uses it.
func (a *Agent) setToolVisibility(v ToolVisibility) {
	a.mu.Lock()
	a.visibility = v
	a.mu.Unlock()
}

// ToolVisibility returns the visibility a component set, or nil.
func (a *Agent) ToolVisibility() ToolVisibility {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.visibility
}

// advertiseLocked resolves the advertisement for reg; a.mu must be held.
func (a *Agent) advertiseLocked(reg Registry, pin bool) func(name string) bool {
	if a.visibility == nil {
		return nil
	}
	return a.visibility.Advertise(reg, pin)
}
