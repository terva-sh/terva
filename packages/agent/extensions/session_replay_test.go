package extensions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

func writeReplayExtension(t *testing.T, home, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock extension uses a shell script")
	}
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
name=%s
printf '{"type":"hello","name":"%%s","version":"1.0","capabilities":["events","tools"]}\n' "$name"
if [ -n "$REPLAY_GATE" ]; then
  while [ ! -f "$REPLAY_GATE" ]; do sleep 0.01; done
fi
printf '%%s\n' '{"type":"subscribe","events":["session_start","session_end"]}'
printf '{"type":"register_tool","name":"rec-%%s","schema":{"type":"object"}}\n' "$name"
printf '%%s\n' '{"type":"ready"}' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"event"'*) printf '%%s\n' "$line" >> "$REPLAY_LOG_DIR/$name";;
    *'"type":"tool_call"'*)
      printf '%%s\n' '{"event":"tool"}' >> "$REPLAY_LOG_DIR/$name"
      id=$(printf '%%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"tool_result","id":"%%s","content":[{"type":"text","text":"ok"}]}\n' "$id"
      ;;
    *'"type":"shutdown"'*) exit 0;;
  esac
done
`, name)
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]string{"name": name, "exec": "./run.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
}

func replayManager(t *testing.T, delayed bool, names ...string) (*Manager, string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("REPLAY_LOG_DIR", home)
	t.Setenv("REPLAY_GATE", "")
	if delayed {
		t.Setenv("REPLAY_GATE", filepath.Join(home, "ready-gate"))
	}
	for _, name := range names {
		writeReplayExtension(t, home, name)
	}
	m := New(home, "", "test", "test", "test", &stubHooks{})
	t.Cleanup(func() { m.Stop(time.Second) })
	if errs := m.Discover(t.Context()); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !delayed {
		m.WaitForReady(testsupport.ExtReadyGrace)
	}
	return m, home
}

func announceReplaySession(m *Manager, id string) extproto.EventFromHost {
	s := SessionIdentity{ID: id, Path: "/sessions/" + id, Title: id, CWD: "/work/" + id, ProjectID: "project-" + id}
	if prev, changed := m.SwapAnnouncedSession(s); changed {
		m.EmitSessionEnd(prev)
	}
	ev := extproto.EventFromHost{Event: "session_start", SessionID: s.ID, SessionPath: s.Path, SessionTitle: s.Title, CWD: s.CWD, ProjectID: s.ProjectID}
	m.EmitEvent(ev)
	return ev
}

func replayRecords(t *testing.T, m *Manager, home, name string) []extproto.EventFromHost {
	t.Helper()
	if _, err := m.InvokeTool(t.Context(), "rec-"+name, json.RawMessage(`{}`), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, name))
	if err != nil {
		t.Fatal(err)
	}
	var records []extproto.EventFromHost
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev extproto.EventFromHost
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		records = append(records, ev)
	}
	return records
}

func requireReplayStarts(t *testing.T, records []extproto.EventFromHost, want int, last extproto.EventFromHost) {
	t.Helper()
	starts := 0
	var got extproto.EventFromHost
	for _, ev := range records {
		if ev.Event == "session_start" {
			starts++
			got = ev
		}
	}
	if starts != want || (want > 0 && (got.SessionID != last.SessionID || got.SessionPath != last.SessionPath || got.SessionTitle != last.SessionTitle || got.CWD != last.CWD || got.ProjectID != last.ProjectID)) {
		t.Fatalf("records = %+v, want %d starts with last %+v", records, want, last)
	}
}

func TestReloadReplaysSessionBeforeTools(t *testing.T) {
	m, home := replayManager(t, false, "alpha")
	want := announceReplaySession(m, "one")
	requireReplayStarts(t, replayRecords(t, m, home, "alpha"), 1, want)
	if stats := m.Reload(t.Context(), time.Second); len(stats.Errors) != 0 || stats.Ready != 1 {
		t.Fatalf("reload = %+v", stats)
	}
	records := replayRecords(t, m, home, "alpha")
	requireReplayStarts(t, records, 2, want)
	if records[len(records)-2].Event != "session_start" || records[len(records)-1].Event != "tool" {
		t.Fatalf("replacement received tool before its session: %+v", records)
	}
	if s, ok := m.TakeAnnouncedSession(); !ok || s.ID != "one" {
		t.Fatalf("reload lost the manager's active identity: %+v, %v", s, ok)
	}
}

func TestEnableAndRestartReplayOnlyReplacement(t *testing.T) {
	m, home := replayManager(t, false, "alpha", "beta")
	want := announceReplaySession(m, "one")
	requireReplayStarts(t, replayRecords(t, m, home, "alpha"), 1, want)
	requireReplayStarts(t, replayRecords(t, m, home, "beta"), 1, want)
	m.ApplyOne(t.Context(), "alpha", false, time.Second)
	m.ApplyOne(t.Context(), "alpha", true, testsupport.ExtReadyGrace)
	requireReplayStarts(t, replayRecords(t, m, home, "alpha"), 2, want)
	m.ApplyOne(t.Context(), "alpha", true, testsupport.ExtReadyGrace)
	requireReplayStarts(t, replayRecords(t, m, home, "alpha"), 2, want)
	if err := m.RestartExtension(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	requireReplayStarts(t, replayRecords(t, m, home, "alpha"), 3, want)
	requireReplayStarts(t, replayRecords(t, m, home, "beta"), 1, want)
	want = announceReplaySession(m, "two")
	if err := m.RestartExtension(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	requireReplayStarts(t, replayRecords(t, m, home, "alpha"), 5, want)
	requireReplayStarts(t, replayRecords(t, m, home, "beta"), 2, want)
}

func TestLateReadyReplaysCurrentSession(t *testing.T) {
	m, home := replayManager(t, true, "alpha")
	announceReplaySession(m, "old")
	want := announceReplaySession(m, "current")
	if err := os.WriteFile(os.Getenv("REPLAY_GATE"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.WaitForReady(testsupport.ExtReadyGrace)
	records := replayRecords(t, m, home, "alpha")
	requireReplayStarts(t, records, 1, want)
	if len(records) != 2 || records[0].Event != "session_start" || records[1].Event != "tool" {
		t.Fatalf("late subscriber lifecycle order = %+v", records)
	}
}

func TestNoEndedSessionReplay(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			m, home := replayManager(t, false, "alpha")
			starts := 0
			if active {
				announceReplaySession(m, "ended")
				prev, _ := m.TakeAnnouncedSession()
				m.EmitSessionEnd(prev)
				requireReplayStarts(t, replayRecords(t, m, home, "alpha"), 1, extproto.EventFromHost{SessionID: "ended", SessionPath: "/sessions/ended", SessionTitle: "ended", CWD: "/work/ended", ProjectID: "project-ended"})
				starts = 1
			}
			if stats := m.Reload(t.Context(), time.Second); len(stats.Errors) != 0 || stats.Ready != 1 {
				t.Fatalf("reload = %+v", stats)
			}
			records := replayRecords(t, m, home, "alpha")
			count := 0
			for _, ev := range records {
				if ev.Event == "session_start" {
					count++
				}
			}
			if count != starts {
				t.Fatalf("inactive session replayed: %+v", records)
			}
		})
	}
}
