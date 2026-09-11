package ctrlproto

import (
	"encoding/json"
	"testing"
)

// encodedSessionFields marshals info inside a real KindResp frame, the way a
// daemon answers sessions.list, and returns the keys that survived the encode.
// Going through the frame rather than the struct is the point: omitempty is a
// property of the bytes on the wire, and a client only ever sees the bytes.
func encodedSessionFields(t *testing.T, info SessionInfo) map[string]json.RawMessage {
	t.Helper()
	result, err := json.Marshal(SessionsResult{Sessions: []SessionInfo{info}})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	raw, err := json.Marshal(Frame{Kind: KindResp, ID: 1, Result: result})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	var frame struct {
		Result struct {
			Sessions []map[string]json.RawMessage `json:"sessions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if len(frame.Result.Sessions) != 1 {
		t.Fatalf("want 1 session on the wire, got %d", len(frame.Result.Sessions))
	}
	return frame.Result.Sessions[0]
}

// encodedTaskFields is encodedSessionFields for the swarm dashboard pane.
func encodedTaskFields(t *testing.T, info TaskInfo) map[string]json.RawMessage {
	t.Helper()
	result, err := json.Marshal(TaskList{Tasks: []TaskInfo{info}})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	raw, err := json.Marshal(Frame{Kind: KindResp, ID: 1, Result: result})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	var frame struct {
		Result struct {
			Tasks []map[string]json.RawMessage `json:"tasks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if len(frame.Result.Tasks) != 1 {
		t.Fatalf("want 1 task on the wire, got %d", len(frame.Result.Tasks))
	}
	return frame.Result.Tasks[0]
}

// TestOriginIsAbsentFromTheWireWhenUnset is the compatibility guarantee: a
// daemon with no fleet sends no origin key at all, so a shipped client sees
// byte-for-byte what it saw before this field existed. An empty string would
// not do, because a client would then have to tell "" apart from absent.
//
// Each case carries a positive control that sets Origin and demands the key
// appear. Without it an absence assertion passes for the wrong reason: a
// renamed field, a dropped tag, or a helper that decoded nothing all look
// exactly like correct omitempty behaviour.
func TestOriginIsAbsentFromTheWireWhenUnset(t *testing.T) {
	t.Run("session", func(t *testing.T) {
		fields := encodedSessionFields(t, SessionInfo{ID: "abc123"})
		if _, ok := fields["id"]; !ok {
			t.Fatal("no id key on the wire: the helper decoded nothing, so the " +
				"absence check below would pass regardless")
		}
		if got, ok := fields["origin"]; ok {
			t.Errorf("a fleetless daemon put origin on the wire as %s; it must be absent", got)
		}

		fields = encodedSessionFields(t, SessionInfo{ID: "abc123", Origin: "neot"})
		got, ok := fields["origin"]
		if !ok {
			t.Fatal("origin was set but never reached the wire: the json tag is wrong")
		}
		if string(got) != `"neot"` {
			t.Errorf("origin encoded as %s, want \"neot\"", got)
		}
	})

	t.Run("task", func(t *testing.T) {
		fields := encodedTaskFields(t, TaskInfo{ID: "writer-4211", Task: "write the thing"})
		if _, ok := fields["id"]; !ok {
			t.Fatal("no id key on the wire: the helper decoded nothing, so the " +
				"absence check below would pass regardless")
		}
		if got, ok := fields["origin"]; ok {
			t.Errorf("a fleetless daemon put origin on the wire as %s; it must be absent", got)
		}

		fields = encodedTaskFields(t, TaskInfo{ID: "writer-4211", Task: "write the thing", Origin: "neot"})
		got, ok := fields["origin"]
		if !ok {
			t.Fatal("origin was set but never reached the wire: the json tag is wrong")
		}
		if string(got) != `"neot"` {
			t.Errorf("origin encoded as %s, want \"neot\"", got)
		}
	})
}

// TestFederatedIDRoundTrip pins the format a hub mints and proves split undoes
// join for it.
func TestFederatedIDRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		id     string
		want   string
	}{
		{"a lone daemon leaves the id bare", "", "abc123", "abc123"},
		{"a member's session", "neot", "abc123", "neot/abc123"},
		{"a member's swarm agent", "neot", "writer-4211", "neot/writer-4211"},
		{"an id with a slash of its own survives the cut", "neot", "a/b", "neot/a/b"},
		{"an empty id still names its member", "neot", "", "neot/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := JoinFederatedID(c.origin, c.id)
			if got != c.want {
				t.Fatalf("JoinFederatedID(%q, %q) = %q, want %q", c.origin, c.id, got, c.want)
			}
			origin, id := SplitFederatedID(got)
			if origin != c.origin || id != c.id {
				t.Errorf("SplitFederatedID(%q) = (%q, %q), want (%q, %q)",
					got, origin, id, c.origin, c.id)
			}
		})
	}
}

// TestSplitFederatedIDCannotSeeAnOriginlessSlash pins the one input the round
// trip does not survive, so the limit is recorded rather than discovered.
//
// A bare id containing a slash is byte-identical to a federated one, and split
// reports an origin no member ever claimed. This does not arise today: a
// session id is uuid.NewString and a swarm agent id is used as a single path
// segment, so neither carries a slash. If that ever changes, this test fails
// and points at the reason.
func TestSplitFederatedIDCannotSeeAnOriginlessSlash(t *testing.T) {
	origin, id := SplitFederatedID("a/b")
	if origin != "a" || id != "b" {
		t.Fatalf("SplitFederatedID(\"a/b\") = (%q, %q); the documented behaviour is (\"a\", \"b\"). "+
			"If this changed on purpose, the id-format note in federated.go needs rewriting too",
			origin, id)
	}
}

// TestValidOrigin covers the constraint the round trip rests on: the first
// slash in a federated id must be the one join put there.
func TestValidOrigin(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"neot", true},
		{"build-box-2", true},
		{"", false},
		{"neot/extra", false},
		{"/", false},
	}
	for _, c := range cases {
		if got := ValidOrigin(c.origin); got != c.want {
			t.Errorf("ValidOrigin(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// TestAnInvalidOriginBreaksTheRoundTrip is why ValidOrigin exists. It is a
// demonstration, not an endorsement: enrollment refuses such an origin, and
// this pins what it is refusing on behalf of.
func TestAnInvalidOriginBreaksTheRoundTrip(t *testing.T) {
	const origin, id = "neot/extra", "abc123"
	if ValidOrigin(origin) {
		t.Fatal("this test needs an origin ValidOrigin rejects")
	}
	gotOrigin, gotID := SplitFederatedID(JoinFederatedID(origin, id))
	if gotOrigin == origin && gotID == id {
		t.Fatal("an origin with a separator round-tripped; ValidOrigin is now stricter " +
			"than it needs to be and its doc comment is wrong")
	}
}
