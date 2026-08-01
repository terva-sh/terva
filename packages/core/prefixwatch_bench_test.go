package core

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// benchTranscript approximates the measured session: ~1500 messages totalling
// roughly 200K tokens (~800KB), the size at which the ladder's per-request cost
// would actually matter.
func benchTranscript(n int) []provider.Message {
	body := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 12) // ~530B
	out := make([]provider.Message, 0, n)
	for i := 0; i < n; i++ {
		role := provider.RoleAssistant
		if i%2 == 0 {
			role = provider.RoleUser
		}
		out = append(out, provider.Message{
			Role:    role,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("%d %s", i, body)}},
		})
	}
	return out
}

func BenchmarkBuildPrefixLadder(b *testing.B) {
	for _, n := range []int{100, 500, 1500} {
		msgs := benchTranscript(n)
		bytes := 0
		for _, m := range msgs {
			for _, c := range m.Content {
				if tb, ok := c.(provider.TextBlock); ok {
					bytes += len(tb.Text)
				}
			}
		}
		b.Run(fmt.Sprintf("msgs=%d/%dKB", n, bytes/1024), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				buildPrefixLadder(nil, "system prompt", msgs)
			}
		})
	}
}
