package ctrlproto

import (
	"context"

	"terva.sh/terva/packages/i18n"
)

// The dispatch table: one entry per verb, keyed by [Method].
//
// This replaced a 137-arm switch in serve.go, and the reason is not line count.
// The switch carried three things by convention that the table carries by
// construction:
//
//  1. **Completeness.** A verb missing from the switch fell through to `unknown
//     method` — a runtime answer, on a client with no idea why its call vanished.
//     methods_complete_test.go had to PARSE serve.go with go/ast to check for it.
//     Against a map that is a plain lookup: every Method constant must be a key.
//
//  2. **One arm per verb.** The switch grouped related verbs behind a shared
//     outer clause (`case MethodA, MethodB, MethodC:`) that resolved the
//     controller once and then re-switched to bind each verb's own params. A verb
//     named ONLY in the outer list was handled by the inner `default:` — running
//     another verb's handler with its params bound to the wrong struct. That
//     shipped seven times (see the eight `default:`-is-the-last-verb traps in
//     dispatch_test.go's header): personas.delete dispatched as PersonasEdit,
//     binding {name} as a write and overwriting the persona with an empty one.
//     A map key cannot be shared, so there is no outer list to hide in and no
//     inner default to fall into. The bug class is gone rather than guarded.
//
//  3. **Controller resolution.** Each optional arm hand-wrote the same
//     type-assert-or-answer-unsupported preamble. It is now one generic helper,
//     so a new controller cannot get a subtly different unsupported answer — or
//     forget the check and panic on a nil assertion.
//
// What the table does NOT change: group negotiation still happens before lookup
// (a verb whose group was not negotiated must answer `unsupported` without ever
// reaching its handler), and subscribe/unsubscribe are still answered by
// serveState itself — they manage the connection's pumps, not the service.

// handler answers one command frame. It runs only after the frame's method group
// has been negotiated.
type handler func(s *serveState, ctx context.Context, f Frame)

// unsupportedMsg is what a verb answers when the carrier does not implement its
// controller. It is a FUNCTION, not a string, because the table is built once at
// package init while the answer is produced per frame: three of these messages
// are localized, and `i18n.T` evaluated at init would freeze the translation at
// process start instead of following the daemon's live locale. The literal stays
// inside the closure so the i18n extractor still sees it.
type unsupportedMsg func() string

// plain is an unsupported message with no localized form — the surface that
// would show it is web-only today, which is how the arms had it.
func plain(s string) unsupportedMsg { return func() string { return s } }

// base marks a verb on the mandatory [WorkspaceService] surface. Every carrier
// implements it, so this is unreachable; naming it says which surface the entry
// is on rather than passing a bare nil.
var base = plain("")

// receiver resolves the type a verb dispatches to: either the mandatory
// [WorkspaceService] (always present) or an optional controller the carrier may
// not implement. In the second case it answers CodeUnsupported and reports false,
// so the caller returns without touching a nil interface.
//
// Answering here rather than deeper is deliberate: an unimplemented controller is
// a fact about the CARRIER, and a client deserves "this daemon does not do that"
// instead of an error from three layers down about a nil pointer.
func receiver[C any](s *serveState, f Frame, unsupported unsupportedMsg) (C, bool) {
	c, ok := s.svc.(C)
	if !ok {
		var zero C
		s.write(ErrFrame(f.ID, CodeUnsupported, unsupported()))
		return zero, false
	}
	return c, true
}

// The four verb shapes. Each takes the message to answer when the carrier does
// not implement C ([base] for the mandatory surface), and a function that does
// the work with the receiver already resolved.
//
// All four hand the function the whole [Frame] rather than just f.Sess, because
// a handler occasionally needs f.ID or the raw params; the session-scoped ones
// read f.Sess from it, and TestDispatchForwardsTheFrameSession pins that they do.

// do — no params, no result. `s.respond(f.ID, nil, err)`.
func do[C any](unsupported unsupportedMsg, fn func(C, context.Context, Frame) error) handler {
	return func(s *serveState, ctx context.Context, f Frame) {
		c, ok := receiver[C](s, f, unsupported)
		if !ok {
			return
		}
		s.respond(f.ID, nil, fn(c, ctx, f))
	}
}

// get — no params, a result.
func get[C, R any](unsupported unsupportedMsg, fn func(C, context.Context, Frame) (R, error)) handler {
	return func(s *serveState, ctx context.Context, f Frame) {
		c, ok := receiver[C](s, f, unsupported)
		if !ok {
			return
		}
		v, err := fn(c, ctx, f)
		s.respond(f.ID, v, err)
	}
}

// act — params bound into P, no result.
func act[C, P any](unsupported unsupportedMsg, fn func(C, context.Context, Frame, P) error) handler {
	return func(s *serveState, ctx context.Context, f Frame) {
		c, ok := receiver[C](s, f, unsupported)
		if !ok {
			return
		}
		var p P
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		s.respond(f.ID, nil, fn(c, ctx, f, p))
	}
}

// ask — params bound into P, a result.
func ask[C, P, R any](unsupported unsupportedMsg, fn func(C, context.Context, Frame, P) (R, error)) handler {
	return func(s *serveState, ctx context.Context, f Frame) {
		c, ok := receiver[C](s, f, unsupported)
		if !ok {
			return
		}
		var p P
		if err := f.Bind(&p); err != nil {
			s.badReq(f.ID, err)
			return
		}
		v, err := fn(c, ctx, f, p)
		s.respond(f.ID, v, err)
	}
}

// --- unsupported messages, taken verbatim from the arms they replace ---
//
// Grouped by controller, EXCEPT DoctorController: it serves four verbs and each
// answered with its own wording, so the message belongs to the verb here rather
// than to the controller. Collapsing those four into one would have silently
// reworded three user-visible answers.
var (
	noLogin       = unsupportedMsg(func() string { return i18n.T("login not supported") })
	noModelParams = unsupportedMsg(func() string { return i18n.T("editing model settings is not supported here") })
	noContinue    = unsupportedMsg(func() string { return i18n.T("continue is not available here") })

	noCards       = plain("the card library is not available here")
	noCardGroups  = plain("card groups are not available here")
	noCardModel   = plain("per-card default models are not available here")
	noSessGroups  = plain("session groups are not available here")
	noPersonas    = plain("the persona library is not available here")
	noUserPers    = plain("saved user personas are not available here")
	noBackgrounds = plain("scene backgrounds are not available here")
	noWorld       = plain("the World is not available here")
	noCast        = plain("the cast is not available here")
	noDirect      = plain("directed authorship is not available here")
	noDraft       = plain("draft sessions are not available here")
	noNote        = plain("the author's note is not available here")
	noUser        = plain("the user persona is not available here")
	noVariants    = plain("variant cleanup is not available here")
	noSuggest     = plain("reply suggestions are not available here")
	noExport      = plain("session export is not available here")
	noArchive     = plain("session archiving is not available here")
	noWorkflows   = plain("workflow runs are not available here")
	noReplay      = plain("replay not supported")

	// DoctorController's four, one per verb.
	noCardDoctor    = plain("the card doctor is not available here")
	noSessionDoctor = plain("the session doctor is not available here")
	noNextScene     = plain("scene breaks are not available here")
	noRealize       = plain("realize is not available here")
)
