package ctrlproto

import (
	"context"
	"testing"
)

// 🚨 The end-to-end assertion for step 2, through the real server rather than
// through the mask arithmetic: a read-only caller that HAS negotiated
// GroupConversation is still refused `prompt`.
//
// This is the pair of facts groups alone cannot produce. The client asked for
// conversation and got it; the verb it named exists, is dispatched, and lives in
// exactly that group — and it is still refused, because the caller may not cause
// a model call. Nothing about the group check can express that.
func TestServeConnRefusesASpendingVerbToAReadOnlyCaller(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ServeConn(ctx, server, svc, ServerHello("terva-test", "0"), WithAuthority(CapRead))

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession},
	}))
	sh := pull(t, client)
	if sh.Kind != KindHello || sh.Hello == nil {
		t.Fatalf("expected server hello, got %+v", sh)
	}
	// The group WAS negotiated — that is what makes the refusal below meaningful.
	var negotiated bool
	for _, g := range sh.Hello.Groups {
		if g == GroupConversation {
			negotiated = true
		}
	}
	if !negotiated {
		t.Fatal("the server did not offer conversation; this test would pass vacuously")
	}

	push(t, client, mustCmd(t, 1, "s1", MethodPrompt, PromptParams{Text: "spend my money"}))
	r := pull(t, client)
	if r.Error == nil {
		t.Fatalf("prompt was ALLOWED to a read-only caller: %+v", r)
	}
	if r.Error.Code != CodeForbidden {
		t.Errorf("prompt refused with %q, want %q — unsupported would send the caller hunting for a protocol problem", r.Error.Code, CodeForbidden)
	}
}

// ...and the same caller keeps the read verbs, or the role is useless rather
// than restricted.
func TestServeConnAllowsAReadVerbToAReadOnlyCaller(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ServeConn(ctx, server, svc, ServerHello("terva-test", "0"), WithAuthority(CapRead))

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession},
	}))
	pull(t, client) // server hello

	push(t, client, mustCmd(t, 1, "s1", MethodSessionsList, struct{}{}))
	if r := pull(t, client); r.Error != nil {
		t.Fatalf("sessions.list refused to a read-only caller: %+v", r.Error)
	}
}

// A carrier that says nothing about authority keeps the behavior every carrier
// had before authority existed. If this regressed, every in-process and native
// carrier would start refusing verbs.
func TestServeConnWithoutAuthorityServesEverything(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ServeConn(ctx, server, svc, ServerHello("terva-test", "0"))

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation},
	}))
	pull(t, client) // server hello

	push(t, client, mustCmd(t, 1, "s1", MethodPrompt, PromptParams{Text: "hi"}))
	if r := pull(t, client); r.Error != nil && r.Error.Code == CodeForbidden {
		t.Fatalf("a carrier that set no authority was refused: %+v", r.Error)
	}
}
