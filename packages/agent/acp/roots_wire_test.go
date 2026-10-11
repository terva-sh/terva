//go:build terva_acp

package acp

import (
	"slices"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestACPWireForwardsAdditionalDirectories(t *testing.T) {
	f := &fakeFactory{root: testsupport.TempDir(t), client: &textTurnClient{reply: "fixture"}}
	h, _, stop := permSetup(t, f)
	defer stop()
	peer := testsupport.TempDir(t)
	res := h.call(MethodSessionNew, map[string]any{"cwd": testsupport.TempDir(t), "additionalDirectories": []string{peer}})
	id, _ := res["sessionId"].(string)
	if id == "" {
		t.Fatal("no session ID")
	}
	f.loadedMu.Lock()
	got := append([]string(nil), f.lastNewRoots...)
	f.loadedMu.Unlock()
	if !slices.Equal(got, []string{peer}) {
		t.Fatal("session/new dropped peer roots")
	}
	h.call(MethodSessionPromptName, map[string]any{"sessionId": id, "prompt": []map[string]string{{"type": "text", "text": "fixture"}}})
	h.call(MethodSessionLoad, map[string]any{"sessionId": id, "additionalDirectories": []string{peer}})
	f.loadedMu.Lock()
	got = append([]string(nil), f.lastLoadRoots...)
	f.loadedMu.Unlock()
	if !slices.Equal(got, []string{peer}) {
		t.Fatal("session/load dropped peer roots")
	}
}
