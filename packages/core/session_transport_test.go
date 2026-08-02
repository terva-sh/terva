package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A net row round-trips to disk and is skipped by the loader — the same
// forward-compat property as stall and prefix rows. Its whole value is being
// readable AGAINST the usage rows, so the fields must survive verbatim.
func TestNetRowRoundTripsAndIsSkippedOnResume(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-codex", "gpt-5.5", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}))
	must(s.AppendTransport(provider.TransportInfo{
		ConnReused:   true,
		RemoteAddr:   "104.18.32.47:443",
		Proto:        "HTTP/2.0",
		RequestID:    "req_abc",
		Ray:          "8f2d1c-SJC",
		ProcessingMS: 950,
	}))
	must(s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}}))
	must(s.Close())

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(msgs) != 2 {
		t.Fatalf("net row must not enter the transcript: want 2 messages, got %d", len(msgs))
	}

	rec := readNetRow(t, path)
	if !rec.ConnReused || rec.RemoteAddr != "104.18.32.47:443" || rec.Proto != "HTTP/2.0" {
		t.Errorf("connection half did not survive: %+v", rec)
	}
	if rec.RequestID != "req_abc" || rec.Ray != "8f2d1c-SJC" || rec.ProcessingMS != 950 {
		t.Errorf("identity half did not survive: %+v", rec)
	}
}

func TestAppendTransportOnNilSession(t *testing.T) {
	var s *Session
	if err := s.AppendTransport(provider.TransportInfo{ConnReused: true}); err != nil {
		t.Errorf("AppendTransport on a nil session must be a no-op, got %v", err)
	}
}

// The agent relays a provider's EventTransport to its observers — the hook
// persistence hangs the net row on. Driven through a real Prompt so the event
// travels the same stream loop production uses.
func TestTransportObserverFiresFromTheStreamLoop(t *testing.T) {
	want := provider.TransportInfo{ConnReused: true, RemoteAddr: "1.2.3.4:443", Ray: "aa-SJC"}
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		return []provider.Event{
			provider.EventStart{Provider: "scripted"},
			provider.EventTransport{Info: want},
			provider.EventTextDelta{Delta: "ok"},
			provider.EventUsage{Usage: provider.Usage{InputTokens: 10, OutputTokens: 2}},
			provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "ok"}},
			}},
		}, nil
	}}
	a := cacheAwareAgent(t, client)
	var got []provider.TransportInfo
	a.AddTransportObserver(func(ti provider.TransportInfo) { got = append(got, ti) })

	// Core's zero value is off (the shipped ON default lives in the build
	// funnel): the event must be dropped, not relayed.
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("transport relayed with recording off: %+v", got)
	}

	a.SetTransportRecording(true)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("observer saw %+v, want exactly [%+v]", got, want)
	}
}

func readNetRow(t *testing.T, path string) provider.TransportInfo {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var found []provider.TransportInfo
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var row sessionLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == recordNet && row.Net != nil {
			found = append(found, *row.Net)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one net row, got %d", len(found))
	}
	return found[0]
}
