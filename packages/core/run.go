package core

import (
	"context"
	"errors"
	"fmt"

	"terva.sh/terva/packages/provider"
)

// Input is what Run starts a turn from. The set is closed: only this package
// defines inputs, so a new kind is an addition a host's code never has to
// handle.
type Input interface {
	runInput()
}

// PromptInput is a user message, with the turn policy: compaction before the
// turn when the transcript is over its threshold, and one compact-and-retry
// when the provider refuses the request as too large.
type PromptInput struct {
	Text   string
	Images []provider.ImageBlock
	Extras UserMessageExtras
}

// ContinueInput resumes the loop with no new user message, as after a turn
// that ended on a tool call or a cancellation. A non-empty Cue is a stage
// direction for this continuation, as ContinueWithCue takes.
type ContinueInput struct {
	Cue string
}

// ContinueAssistantInput asks the model to extend its last assistant message
// rather than answer it.
type ContinueAssistantInput struct{}

func (PromptInput) runInput()            {}
func (ContinueInput) runInput()          {}
func (ContinueAssistantInput) runInput() {}

// Run starts a turn from in and streams its events to sink, which may be nil.
// It is the one entry point to extend: a new way to start a turn is a new
// Input, not a new method. It returns ErrBusy when a turn is already running,
// as the methods it stands in for do.
//
// A pointer to an input is an Input too, because the marker method has a
// value receiver, so Run takes either form and treats a nil pointer as a nil
// input.
func (a *Agent) Run(ctx context.Context, in Input, sink func(AgentEvent)) error {
	switch p := in.(type) {
	case *PromptInput:
		in = derefInput(p)
	case *ContinueInput:
		in = derefInput(p)
	case *ContinueAssistantInput:
		in = derefInput(p)
	}
	switch in := in.(type) {
	case PromptInput:
		return a.PromptWithPolicyExtra(ctx, in.Text, in.Images, in.Extras, sink)
	case ContinueInput:
		if in.Cue == "" {
			return a.Continue(ctx, sink)
		}
		return a.ContinueWithCue(ctx, sink, in.Cue)
	case ContinueAssistantInput:
		return a.ContinueAssistant(ctx, sink)
	case nil:
		return errors.New("core: Run: input is nil")
	}
	return fmt.Errorf("core: Run: unknown input %T", in)
}

// derefInput returns the input p points to, or nil for a nil p.
func derefInput[T Input](p *T) Input {
	if p == nil {
		return nil
	}
	return *p
}
