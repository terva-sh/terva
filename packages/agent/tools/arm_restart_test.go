package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/restartmarker"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestArmRestartWritesMarker(t *testing.T) {
	home := testsupport.TempDir(t)
	tool := &ArmRestartTool{Session: "sess-42", Home: home}

	before := time.Now()
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"reason":"apply unit change","window_seconds":30}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("arm returned an error result: %+v", res)
	}

	m, ok := restartmarker.Read(home, time.Now())
	if !ok {
		t.Fatal("no valid marker written")
	}
	if m.Session != "sess-42" {
		t.Errorf("marker session = %q, want sess-42", m.Session)
	}
	if m.Reason != "apply unit change" {
		t.Errorf("marker reason = %q", m.Reason)
	}
	if m.Nonce == "" {
		t.Error("marker has no nonce")
	}
	// Expiry is ~30s out (allow slack for the two time.Now reads).
	wantMin := before.Add(30 * time.Second).Unix()
	wantMax := time.Now().Add(30 * time.Second).Unix()
	if m.ExpiresUnix < wantMin || m.ExpiresUnix > wantMax {
		t.Errorf("expiry %d outside [%d,%d]", m.ExpiresUnix, wantMin, wantMax)
	}
}

func TestArmRestartWindowClamped(t *testing.T) {
	cases := []struct {
		name          string
		args          string
		wantAtLeast   time.Duration
		wantAtMostSec int64
	}{
		{"default when omitted", `{}`, armDefaultWindow, int64(armDefaultWindow/time.Second) + 2},
		{"clamped to max", `{"window_seconds":9999}`, armMaxWindow, int64(armMaxWindow/time.Second) + 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := testsupport.TempDir(t)
			tool := &ArmRestartTool{Session: "s", Home: home}
			now := time.Now()
			if _, err := tool.Execute(context.Background(), json.RawMessage(c.args), nil); err != nil {
				t.Fatal(err)
			}
			m, ok := restartmarker.Read(home, now)
			if !ok {
				t.Fatal("no marker")
			}
			gotWindow := m.ExpiresUnix - now.Unix()
			if gotWindow < int64(c.wantAtLeast/time.Second) || gotWindow > c.wantAtMostSec {
				t.Errorf("window %ds, want ~%v (<= %ds)", gotWindow, c.wantAtLeast, c.wantAtMostSec)
			}
		})
	}
}

func TestArmRestartResultMentionsWindow(t *testing.T) {
	home := testsupport.TempDir(t)
	tool := &ArmRestartTool{Session: "s", Home: home}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"window_seconds":5}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	tb, ok := res.Content[0].(provider.TextBlock)
	if !ok {
		t.Fatalf("result content[0] = %T, want provider.TextBlock", res.Content[0])
	}
	if !strings.Contains(tb.Text, "5s") {
		t.Errorf("result %q does not mention the 5s window", tb.Text)
	}
}
