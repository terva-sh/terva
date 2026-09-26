package session

import (
	"fmt"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// BenchmarkDescribeSessions measures the session-listing scan (the /sessions
// picker and the startup scan) over a directory of many sessions. The cost
// grows with session count, so it's worth a repeatable number before a release.
func BenchmarkDescribeSessions(b *testing.B) {
	root := testsupport.TempDir(b)
	const cwd = "/bench/ws"
	const sessions, msgsPer = 120, 24
	for s := range sessions {
		sess, err := NewSession(root, cwd, "openai-codex", "gpt-5.5", "0.0.0")
		if err != nil {
			b.Fatal(err)
		}
		for m := range msgsPer {
			if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{
				provider.TextBlock{Text: fmt.Sprintf("message %d in session %d", m, s)},
			}}); err != nil {
				b.Fatal(err)
			}
		}
		if err := sess.Close(); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	for b.Loop() {
		if got := DescribeSessions(root, cwd); len(got) != sessions {
			b.Fatalf("DescribeSessions = %d summaries, want %d", len(got), sessions)
		}
	}
}
