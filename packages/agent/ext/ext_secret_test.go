package ext

import (
	"encoding/json"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

// The four public Secret APIs were dead on arrival in every release from
// v0.131.2 through v0.131.10.
//
// The host answered correctly. The SDK's Run loop routed ext-initiated replies
// by a hand-written list of three verb names — host_tool_result, session_list,
// session_data — so secret_value, secret_keys and secret_ack all fell into the
// `default: unknown frame type` arm. Every call blocked the full 30s request
// timeout and returned "timed out waiting for host reply", an error that blames
// the host for a reply it had already sent. The observable behaviour pushed
// extension authors back to the plaintext token storage the feature exists to
// eliminate.
//
// Nothing caught it because nothing exercised a secret verb end to end. These
// tests do, one per verb, and they are fast: a broken route shows up as a 2s
// harness timeout rather than a wrong value.

func TestSecretGetRoundTrip(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	type result struct {
		val   string
		found bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		v, ok, err := h.ext.Secret("api-token")
		done <- result{v, ok, err}
	}()

	f := h.drainUntil(t, "secret_get")
	var req extproto.SecretGetFromExt
	if err := json.Unmarshal(f.raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Key != "api-token" {
		t.Errorf("key = %q, want api-token", req.Key)
	}
	if req.ID == "" {
		t.Fatal("secret_get needs a correlation id")
	}

	h.sendToExt(t, extproto.SecretValueFromHost{
		Type: "secret_value", ID: req.ID, Value: "s3cret", Found: true,
	})

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Secret: %v", r.err)
		}
		if r.val != "s3cret" || !r.found {
			t.Errorf("Secret = (%q,%v), want (s3cret,true)", r.val, r.found)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Secret never returned — the secret_value reply was not routed to the waiting call")
	}
}

func TestSecretSetRoundTrip(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	done := make(chan error, 1)
	go func() { done <- h.ext.SetSecret("api-token", "s3cret") }()

	f := h.drainUntil(t, "secret_set")
	var req extproto.SecretSetFromExt
	if err := json.Unmarshal(f.raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Key != "api-token" || req.Value != "s3cret" {
		t.Errorf("secret_set = (%q,%q), want (api-token,s3cret)", req.Key, req.Value)
	}

	h.sendToExt(t, extproto.SecretAckFromHost{Type: "secret_ack", ID: req.ID})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetSecret: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SetSecret never returned — the secret_ack reply was not routed to the waiting call")
	}
}

func TestSecretDeleteRoundTrip(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	done := make(chan error, 1)
	go func() { done <- h.ext.DeleteSecret("api-token") }()

	f := h.drainUntil(t, "secret_delete")
	var req extproto.SecretDeleteFromExt
	if err := json.Unmarshal(f.raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	h.sendToExt(t, extproto.SecretAckFromHost{Type: "secret_ack", ID: req.ID})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DeleteSecret: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DeleteSecret never returned — the secret_ack reply was not routed to the waiting call")
	}
}

func TestSecretKeysRoundTrip(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	type result struct {
		keys []string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		k, err := h.ext.SecretKeys()
		done <- result{k, err}
	}()

	f := h.drainUntil(t, "secret_list")
	var req extproto.SecretListFromExt
	if err := json.Unmarshal(f.raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	h.sendToExt(t, extproto.SecretKeysFromHost{
		Type: "secret_keys", ID: req.ID, Keys: []string{"a", "b"},
	})

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("SecretKeys: %v", r.err)
		}
		if len(r.keys) != 2 || r.keys[0] != "a" || r.keys[1] != "b" {
			t.Errorf("SecretKeys = %v, want [a b]", r.keys)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SecretKeys never returned — the secret_keys reply was not routed to the waiting call")
	}
}

// TestReplyRoutingIsNotAVerbList is the structural guard behind the four above.
//
// Those tests would all pass again if someone fixed the routing by adding three
// more names to the case list — and the NEXT reply verb would break exactly the
// same way. This asserts the property that actually prevents recurrence: a frame
// carrying a pending correlation id is delivered to its waiter even when its
// verb is one the read loop has never heard of.
func TestReplyRoutingIsNotAVerbList(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	// Register a waiter the way every request path does, then answer it with a
	// verb that appears in no case label anywhere in the read loop.
	id := h.ext.nextID()
	ch := make(chan json.RawMessage, 1)
	h.ext.pendingMu.Lock()
	h.ext.pending[id] = ch
	h.ext.pendingMu.Unlock()

	h.sendToExt(t, map[string]any{
		"type":  "some_verb_invented_after_this_test_was_written",
		"id":    id,
		"value": "delivered",
	})

	select {
	case line := <-ch:
		var got struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("unmarshal delivered reply: %v", err)
		}
		if got.Value != "delivered" {
			t.Errorf("delivered payload = %q, want %q", got.Value, "delivered")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a frame carrying a PENDING id was not delivered to its waiter. " +
			"Reply routing has gone back to matching on a list of verb names, which is how all four " +
			"Secret APIs shipped dead across nine releases.")
	}
}
