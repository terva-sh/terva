// chat-loopback — the smallest honest dual-role plugin: one process that
// is BOTH an extension (the agent calls into it) and a chat connector
// (it triggers the agent), demonstrating the connector role added in
// extension protocol 5 (docs/proposals/connector-extensions.md).
//
// The transport implements connsdk.Transport — the SAME interface a
// standalone `terva bot` connector implements. That is the tunnel
// design's point: this fileTransport could be lifted verbatim into a
// standalone connector main (connsdk.Main) or stay here sharing a
// process with the extension half; the wire between it and terva is
// identical either way.
//
// The "chat service" is your filesystem, so the demo needs no external
// account:
//
//   - inbound  — every file dropped into <data-dir>/inbox/ becomes one
//     chat message (file contents = text) and starts an agent turn;
//   - outbound — the agent's replies append to <data-dir>/outbox.txt.
//
// The point of being ONE process: the loopback_stats tool and the
// transport share live in-memory state (message counters, the last
// message seen). Ask the agent "call loopback_stats" mid-conversation
// and it reads the same session the connector half is running — no
// second process, no state file handshake.
//
// Try it (the connector role activates only when you select it):
//
//	terva ext install ./examples/extensions/chat-loopback
//	terva bot run --connector chat-loopback
//	# in another shell (see `terva bot logs` for the exact data dir):
//	echo "/start" > "$TERVA_HOME/ext-data/chat-loopback/inbox/000-start.txt"
//	echo "hi! what tools do you have?" > "$TERVA_HOME/ext-data/chat-loopback/inbox/001-hi.txt"
//	tail -f "$TERVA_HOME/ext-data/chat-loopback/outbox.txt"
//
// Echo-loop hygiene (the transport author's job): inbound is only ever
// read from inbox/, and replies are only ever written to outbox.txt, so
// the bot can never hear itself speak.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/agent/ext"
)

// stats is the shared state both roles touch: the transport mutates it,
// the tool reads it. One process ⇒ one source of truth.
type stats struct {
	mu       sync.Mutex
	received int
	sent     int
	lastText string
	lastFrom string
}

func main() {
	e := ext.New("chat-loopback", "1.0.0")
	st := &stats{}

	// --- extension half: a tool over the connector's live state.
	e.Tool("loopback_stats",
		"Live statistics of the loopback chat session this same process is running: messages received/sent and the last inbound message.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(args json.RawMessage) ext.ToolResult {
			st.mu.Lock()
			defer st.mu.Unlock()
			return ext.TextResult(fmt.Sprintf(
				"received=%d sent=%d last_from=%q last_text=%q",
				st.received, st.sent, st.lastFrom, st.lastText))
		},
		ext.ReadOnly(),
	)

	// --- connector half: the filesystem transport, built lazily per
	// chat session; the connsdk.Session carries the host-assigned data
	// dir, exactly as for a standalone connector.
	e.Connector(connsdk.Capabilities{MaxTextLen: 4000}, func(s connsdk.Session) (connsdk.Transport, error) {
		if s.DataDir == "" {
			return nil, fmt.Errorf("no data dir assigned")
		}
		return &fileTransport{
			st:     st,
			inbox:  filepath.Join(s.DataDir, "inbox"),
			outbox: filepath.Join(s.DataDir, "outbox.txt"),
		}, nil
	})

	if err := e.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "chat-loopback:", err)
		os.Exit(1)
	}
}

// fileTransport implements connsdk.Transport over inbox/ + outbox.txt
// under the host-assigned data dir.
type fileTransport struct {
	st *stats

	inbox  string
	outbox string
}

func (t *fileTransport) Connect(ctx context.Context) (connsdk.Identity, error) {
	if err := os.MkdirAll(t.inbox, 0o755); err != nil {
		return connsdk.Identity{}, err
	}
	return connsdk.Identity{ID: "loopback-bot", Username: "loopback"}, nil
}

// Receive polls inbox/ and delivers each file as one message (name-
// ordered, so 000-a.txt precedes 001-b.txt), then deletes it.
func (t *fileTransport) Receive(ctx context.Context, deliver func(connsdk.Message)) error {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		entries, err := os.ReadDir(t.inbox)
		if err != nil {
			continue // inbox may briefly not exist; not fatal
		}
		names := make([]string, 0, len(entries))
		for _, ent := range entries {
			if !ent.IsDir() && !strings.HasPrefix(ent.Name(), ".") {
				names = append(names, ent.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(t.inbox, name)
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			_ = os.Remove(path)
			text := strings.TrimSpace(string(b))
			if text == "" {
				continue
			}
			t.st.mu.Lock()
			t.st.received++
			t.st.lastText = text
			t.st.lastFrom = "local-user"
			n := t.st.received
			t.st.mu.Unlock()
			// Protocol 2 wants a stable per-chat message id; a service
			// with none (the filesystem) mints one.
			deliver(connsdk.Message{ID: fmt.Sprintf("in-%d", n), TS: time.Now().UnixMilli(),
				ChatID: "loopback", UserID: "local-user", Username: "local-user", Text: text})
		}
	}
}

func (t *fileTransport) Send(ctx context.Context, out connsdk.Outgoing) error {
	t.st.mu.Lock()
	t.st.sent++
	t.st.mu.Unlock()
	f, err := os.OpenFile(t.outbox, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("15:04:05"), out.Text)
	return err
}

func (t *fileTransport) SendImage(ctx context.Context, chatID, path, caption string) error {
	return fmt.Errorf("loopback sends text only")
}

func (t *fileTransport) SendFile(ctx context.Context, chatID, path, caption string) error {
	return fmt.Errorf("loopback sends text only")
}

func (t *fileTransport) Typing(ctx context.Context, chatID string) error { return nil }
