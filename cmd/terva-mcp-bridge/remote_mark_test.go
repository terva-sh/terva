package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The bridge, not the remote, decides the mark. Whatever the remote put at the
// key, in any letter case, the bridge leaves one exact key holding true, and it
// leaves every other field and _meta key as the remote sent it.
func TestMarkRemoteOverwritesWhatTheRemoteSent(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantMeta map[string]any
	}{
		{"no _meta", `{"protocolVersion":"2025-06-18"}`, map[string]any{remoteMetaKey: true}},
		{"remote says false", `{"_meta":{"terva.sh/remote":false,"other":1}}`, map[string]any{remoteMetaKey: true, "other": 1.0}},
		{"remote says a string", `{"_meta":{"terva.sh/remote":"no"}}`, map[string]any{remoteMetaKey: true}},
		{"case variants", `{"_meta":{"TERVA.SH/REMOTE":false,"Terva.sh/Remote":true}}`, map[string]any{remoteMetaKey: true}},
		{"duplicate key, false last", `{"_meta":{"terva.sh/remote":true,"terva.sh/remote":false}}`, map[string]any{remoteMetaKey: true}},
		{"null _meta", `{"_meta":null}`, map[string]any{remoteMetaKey: true}},
		{"string _meta", `{"_meta":"x"}`, map[string]any{remoteMetaKey: true}},
		{"array _meta", `{"_meta":[{"terva.sh/remote":false}]}`, map[string]any{remoteMetaKey: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := markRemote(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("markRemote(%s): %v", c.in, err)
			}
			var got struct {
				ProtocolVersion string         `json:"protocolVersion"`
				Meta            map[string]any `json:"_meta"`
			}
			// A fresh struct per case, so no value carries over from another.
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("output %s does not parse: %v", out, err)
			}
			if !reflect.DeepEqual(got.Meta, c.wantMeta) {
				t.Errorf("_meta = %v, want %v (output %s)", got.Meta, c.wantMeta, out)
			}
			if strings.Contains(c.in, "protocolVersion") && got.ProtocolVersion != "2025-06-18" {
				t.Errorf("protocolVersion did not pass through: %s", out)
			}
		})
	}
}

// A result that cannot hold the mark stops the bridge, because relaying it
// unmarked would make the remote's read-only tools local reads to terva.
func TestMarkRemoteRefusesAResultThatIsNotAnObject(t *testing.T) {
	for _, in := range []string{``, `null`, `[]`, `"x"`, `1`, `{`} {
		if out, err := markRemote(json.RawMessage(in)); err == nil {
			t.Errorf("markRemote(%q) = %s, want an error", in, out)
		}
	}
}

// The stdio face cannot exist without the mark: the constructor that serve
// needs is the one that adds it.
func TestTheStdioServerRefusesAnUnmarkableResult(t *testing.T) {
	if _, err := newStdioServer(nil, json.RawMessage(`[]`), strings.NewReader(""), &syncBuffer{}); err == nil {
		t.Fatal("newStdioServer accepted an initialize result it cannot mark")
	}
}
