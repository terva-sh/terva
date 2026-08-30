package tools

import "context"

// scriptCallKey marks a tool dispatch that originated in a script binding
// — code_execution's read/grep/glob, code_execution_mutating's write/edit,
// or a disclosed tool reached through call(). It is a plain context value
// rather than a field on any tool, because the crossing it describes is a
// property of one dispatch, not of the tool being dispatched.
//
// This file is deliberately untagged even though only the terva_scripting
// build sets the marker: read.go consults it in every build, and a
// tag-gated helper would make the read tool's dedup condition depend on
// the build tag rather than on the call.
type scriptCallKey struct{}

// withScriptCall marks ctx as a dispatch whose result goes to a running
// script, NOT into the model's transcript.
//
// The distinction matters to any tool that elides output on the theory
// that the model already has a copy. read's dedup is the case in hand: it
// answers a repeat read with a sentence ("unchanged since you read it
// earlier this session; the copy above is still current") instead of the
// bytes. For a model-issued read that trade is sound and saves real
// context. For a script it is false on both halves — the earlier bytes
// never entered the model's context, and the sentence arrives as the
// return value of read(), where the program parses it as file content.
// A session recorded in docs/reviews/2026-08-29-local-model-harness-friction-review.md
// lost 28 turns to that, the model eventually walking its offset/limit
// arguments by one per call to force a cache miss.
//
// code_execution's own description already states the premise this marker
// encodes: "The results of the calls in the program do not enter your
// context."
func withScriptCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, scriptCallKey{}, true)
}

// isScriptCall reports whether this dispatch came from a script binding,
// and so whether its result bypasses the model's transcript.
func isScriptCall(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(scriptCallKey{}).(bool)
	return v
}
