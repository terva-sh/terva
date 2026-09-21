package ctrlproto

import (
	"context"
	"testing"
)

// provisioned is the verb set an embedded consumer actually drives: open a
// session, wait for its tools, prompt it, watch for the turn ending. It is the
// worked example from the option's doc comment, and it is deliberately the
// awkward case — one spending verb and no others.
var provisioned = []Method{MethodSessionCreate, MethodContextGet, MethodPrompt, MethodSessionsList}

// 🚨 The fact that motivates the third gate, asserted rather than argued: the
// capability axis cannot separate `prompt` from the other spending verbs,
// because capabilities name a CLASS and these four share it.
//
// If this ever fails because someone reclassified one of them, the option's
// premise has changed and its doc comment is wrong — read it before editing
// this test.
func TestSpendingVerbsAreCapabilityIdenticalToPrompt(t *testing.T) {
	want := MethodPrompt.Capabilities()
	for _, m := range []Method{MethodSuggestNextStep, MethodSideChatAsk, MethodSessionsDoctor, MethodQueue} {
		if got := m.Capabilities(); got != want {
			t.Errorf("%s has capabilities %d, prompt has %d; the mask could now separate them", m, got, want)
		}
	}
	// And therefore the minimum mask a consumer of `provisioned` can hold is
	// the full set, which admits every verb above.
	var minimum Capability
	for _, m := range provisioned {
		minimum |= m.Capabilities()
	}
	if minimum != capAll {
		t.Fatalf("the minimum mask is %d, not capAll; the premise has changed", minimum)
	}
	for _, m := range []Method{MethodSuggestNextStep, MethodSideChatAsk, MethodSessionsDoctor} {
		if !m.Permits(minimum) {
			t.Errorf("%s is no longer admitted by the minimum mask; the premise has changed", m)
		}
	}
}

// A provisioned caller keeps every verb it was given, or the option is useless
// rather than restrictive.
func TestServeConnAllowsEveryProvisionedVerb(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ServeConn(ctx, server, svc, ServerHello("terva-test", "0"), WithMethods(provisioned...))

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession},
	}))
	pull(t, client) // server hello

	id := uint64(0)
	for _, m := range []Method{MethodSessionsList, MethodSessionCreate, MethodContextGet, MethodPrompt} {
		id++
		push(t, client, mustCmd(t, id, "s1", m, PromptParams{Text: "provisioned"}))
		if r := pull(t, client); r.Error != nil && r.Error.Code == CodeForbidden {
			t.Errorf("%s was refused to the caller provisioned for it: %+v", m, r.Error)
		}
	}
}

// The verb this exists for. `suggest.next_step` is negotiated, permitted by the
// only mask the caller can hold, and still refused — which neither of the other
// two gates can produce.
func TestServeConnRefusesAnUnprovisionedSpendingVerb(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// capAll, because that is the minimum a prompt-driving caller can hold.
	go ServeConn(ctx, server, svc, ServerHello("terva-test", "0"), WithAuthority(capAll), WithMethods(provisioned...))

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession},
	}))
	sh := pull(t, client)
	// The group WAS negotiated and the mask DOES permit it, or the refusal
	// below would prove nothing about this gate.
	var negotiated bool
	for _, g := range sh.Hello.Groups {
		if g == MethodSuggestNextStep.Group() {
			negotiated = true
		}
	}
	if !negotiated {
		t.Fatal("suggest.next_step's group was not negotiated; this test would pass vacuously")
	}
	if !MethodSuggestNextStep.Permits(capAll) {
		t.Fatal("suggest.next_step is not permitted by capAll; this test would pass vacuously")
	}

	push(t, client, mustCmd(t, 1, "s1", MethodSuggestNextStep, struct{}{}))
	r := pull(t, client)
	if r.Error == nil {
		t.Fatalf("suggest.next_step was allowed to a caller not provisioned for it: %+v", r)
	}
	if r.Error.Code != CodeForbidden {
		t.Errorf("refused with %q, want %q — the verb exists and is negotiated", r.Error.Code, CodeForbidden)
	}
}

// subscribe and unsubscribe are handled before the dispatch table but AFTER the
// gates, so a provisioned set that omits them refuses them like anything else.
// Pinned because their early return is the kind of thing a later edit moves.
func TestServeConnRefusesUnprovisionedSubscribe(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ServeConn(ctx, server, svc, ServerHello("terva-test", "0"), WithMethods(provisioned...))

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession},
	}))
	pull(t, client)

	push(t, client, mustCmd(t, 1, "s1", MethodSubscribe, struct{}{}))
	r := pull(t, client)
	if r.Error == nil || r.Error.Code != CodeForbidden {
		t.Fatalf("subscribe reached the event pump without being provisioned: %+v", r)
	}
}

// A carrier that says nothing keeps the behavior every carrier had before this
// existed. The permissive default is what makes the option additive.
func TestServeConnWithoutMethodsAllowsEverything(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ServeConn(ctx, server, svc, ServerHello("terva-test", "0"))

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession},
	}))
	pull(t, client)

	push(t, client, mustCmd(t, 1, "s1", MethodSuggestNextStep, struct{}{}))
	if r := pull(t, client); r.Error != nil && r.Error.Code == CodeForbidden {
		t.Fatalf("an unrestricted carrier refused a verb: %+v", r.Error)
	}
}

// An empty set is "say nothing", not "allow nothing". The opposite reading would
// turn a caller that passes a computed-empty list into a connection that serves
// no verb at all, which is a failure mode worth naming.
func TestWithMethodsEmptyIsPermissive(t *testing.T) {
	s := &serveState{}
	WithMethods()(s)
	if s.methods != nil {
		t.Fatal("an empty set produced a restriction; it must mean no restriction")
	}
}

// The set is copied, so a caller that keeps and mutates its own slice cannot
// widen a connection that is already serving.
func TestWithMethodsCopiesTheSet(t *testing.T) {
	given := []Method{MethodSessionsList}
	s := &serveState{}
	WithMethods(given...)(s)
	given[0] = MethodPrompt
	if s.methods[MethodPrompt] {
		t.Fatal("mutating the caller's slice widened the connection")
	}
	if !s.methods[MethodSessionsList] {
		t.Fatal("the original verb was lost")
	}
}
