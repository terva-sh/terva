package core

import "strings"

// A Frame is the context a host gives the engine for one request: an ordered
// list of segments. The engine does not know what any segment says or why it is
// there. It reads three facts about a frame, because they are facts about
// providers rather than conventions of any host: the order of the segments, the
// stability of each, and its size. The prompt's structure (which sections a
// system prompt has, what rides after the transcript, how either is guarded)
// belongs to the host that assembles the frame. terva's assembler is
// build.Assembler; a host with no conventions of its own can use StaticSystem.
//
// Decision 0021, rule 2, and decision 0010, which recorded that the tail's
// untyped composition contract had come to need this structure.
type Frame struct {
	Segments []Segment
}

// Segment is one piece of a Frame.
//
// Stability decides where the segment goes. Stable segments, in order, form the
// system prompt: the cached prefix a provider matches byte for byte, so a
// change to one costs a full-price read of the transcript. Volatile segments
// ride after the transcript in the ephemeral tail, which is never cached and
// never written to the transcript, ahead of the tail blocks the engine adds
// itself (see tail.go).
//
// There is no role field. The wire has two places for context today, and
// stability picks between them. A role arrives when the wire gains a third.
//
// Tag is the host's name for the segment. The engine never reads it for a
// decision: it names the segment in events, in the recorded tail (a Volatile
// segment's tag is its TailBlock ID), and in a size view such as /context.
type Segment struct {
	Stability Stability
	Tag       string
	Content   string
}

// Stability is whether a segment belongs to the cached prefix.
type Stability int

const (
	// Stable content is expected to hold across requests and is sent in the
	// cached prefix. The engine pins it for a whole turn segment, so a host
	// changing it mid-turn affects the next turn, not the steps in flight.
	Stable Stability = iota
	// Volatile content may change on every request. It is sent after the
	// transcript and never cached.
	Volatile
)

// AssembleMode says whether an Assemble call is for a request about to be
// sent or for a look at one.
type AssembleMode int

const (
	// AssembleRequest is a frame for a request the engine is about to send. The
	// assembler may do the per-request work that implies, such as recording
	// which lore entries fired. The engine asks once per model request.
	AssembleRequest AssembleMode = iota
	// AssemblePeek is a frame for inspection: the /context size view, or the
	// engine's own prefix pin and prefix-change guard. It must render what
	// AssembleRequest would, without its side effects.
	AssemblePeek
)

// ContextAssembler produces the Frame for each request. It replaces the
// System field and the ContextProvider pair.
//
// The engine calls it outside its own lock, because an assembler may read the
// agent. It is called from more than one goroutine: the turn loop, and any
// reader of FramePreview. A host that changes its frame live, as terva does on
// a lore or trust reload and a tools rebuild, does so inside its assembler
// under the assembler's own lock. The engine has no method to swap one.
type ContextAssembler interface {
	Assemble(mode AssembleMode) Frame
}

// TailDeliveryObserver is an optional interface for a ContextAssembler. An
// assembler that implements it hears, once per model request, which tail
// blocks that request carried: its own Volatile segments by tag, then the
// blocks the engine added. Engine blocks have their IDs from tail.go.
//
// It exists for a Volatile segment that should be shown once. Only the engine
// can say whether a composed segment was sent. A continue turn suppresses the
// whole tail, so ids is empty there, and a segment the host assembled for that
// request never reached the model. A retried attempt re-assembles, so a
// segment is spent by the attempt that was sent, not by the one that was
// built.
//
// The call comes after the request has reached the provider and before any of
// the reply streams, on the turn loop's goroutine and outside the agent's lock.
// Being sent is not the same as being answered: a prompt withdrawn afterwards
// (EvUserMessageWithdrawn) took back a turn the model produced nothing for.
type TailDeliveryObserver interface {
	TailDelivered(ids []string)
}

// StaticSystem is a ContextAssembler holding one fixed Stable segment tagged
// "system", and no Volatile segments: the whole of what a host that only has a
// system prompt needs. An empty text gives an empty frame.
func StaticSystem(text string) ContextAssembler { return staticSystem(text) }

type staticSystem string

func (s staticSystem) Assemble(AssembleMode) Frame {
	if s == "" {
		return Frame{}
	}
	return Frame{Segments: []Segment{{Stability: Stable, Tag: "system", Content: string(s)}}}
}

// SystemText joins the Stable segments, in order, into the text that rides
// provider.Request.System. Segments are separated by a blank line. Empty
// segments are joined like any other, so a host that renders its prompt with
// the same separator gets the same bytes back.
func (f Frame) SystemText() string {
	var parts []string
	for _, s := range f.Segments {
		if s.Stability == Stable {
			parts = append(parts, s.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Volatile returns the Volatile segments as tail blocks, in order, each named
// by its tag. Segments with empty content are dropped, as an empty context
// provider added nothing to the tail.
func (f Frame) Volatile() []TailBlock {
	var out []TailBlock
	for _, s := range f.Segments {
		if s.Stability == Volatile && s.Content != "" {
			out = append(out, TailBlock{ID: s.Tag, Text: s.Content})
		}
	}
	return out
}

// VolatileText is the Volatile segments joined as they ride the tail, without
// the blocks the engine adds itself: what the host contributes to a request.
func (f Frame) VolatileText() string { return tailText(f.Volatile()) }
