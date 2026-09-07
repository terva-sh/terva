package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func hintInteractive(msgs []provider.Message) *Interactive {
	i := &Interactive{}
	i.armCarrierBind()
	i.mu.Lock()
	i.carrierMessages = msgs
	i.mu.Unlock()
	return i
}

func statusOK(i *Interactive) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.statusOK
}

// TestResumeHintNamesTheCommandWhenStuck is the discoverability half of the
// feature. A user who reopens a session that died mid-turn sees their own
// message and nothing else; without this line there is nothing on screen that
// says a command exists, let alone which one.
func TestResumeHintNamesTheCommandWhenStuck(t *testing.T) {
	for _, tc := range []struct {
		what string
		msgs []provider.Message
		want string
	}{
		{
			"a prompt that never got an answer",
			[]provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}},
			"without a reply",
		},
		{
			"a reply the provider cut short",
			[]provider.Message{
				{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}},
				{
					Role:    provider.RoleAssistant,
					Content: []provider.Content{provider.TextBlock{Text: "I was say"}},
					Meta:    map[string]string{core.MetaIncomplete: "true"},
				},
			},
			"stopped partway",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			i := hintInteractive(tc.msgs)
			i.hintResumeOnBind()
			got := statusOK(i)
			if !strings.Contains(got, "/continue") {
				t.Errorf("hint = %q; it must name the command, or it is not a hint", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("hint = %q; want it to say %q so the two shapes read differently", got, tc.want)
			}
		})
	}
}

// TestResumeHintStaysQuietOnAHealthySession. A hint that fires when nothing is
// wrong is worse than none: it trains people to ignore the line, and the one
// time it matters they will.
func TestResumeHintStaysQuietOnAHealthySession(t *testing.T) {
	i := hintInteractive([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a complete answer"}}},
	})
	i.hintResumeOnBind()
	if got := statusOK(i); got != "" {
		t.Errorf("hint = %q on a session whose last turn finished; want silence", got)
	}
}

// TestResumeHintIsOneShotPerBinding is the reason the arm exists. A snapshot
// also arrives at the end of every turn and after every compact and clear. If
// the hint fired on each of those it would appear after ordinary replies, which
// is both wrong and the fastest way to make the line invisible.
func TestResumeHintIsOneShotPerBinding(t *testing.T) {
	stuck := []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}}
	i := hintInteractive(stuck)

	i.hintResumeOnBind()
	if got := statusOK(i); !strings.Contains(got, "/continue") {
		t.Fatalf("first snapshot hint = %q; want the offer", got)
	}

	// A later snapshot on the SAME binding, with the transcript still stuck.
	i.mu.Lock()
	i.statusOK = ""
	i.mu.Unlock()
	i.hintResumeOnBind()
	if got := statusOK(i); got != "" {
		t.Errorf("a later snapshot re-fired the hint (%q); it is armed per binding, not per snapshot", got)
	}

	// Binding to another session arms it again, because that is a different
	// conversation with its own answer.
	i.armCarrierBind()
	i.hintResumeOnBind()
	if got := statusOK(i); !strings.Contains(got, "/continue") {
		t.Errorf("a fresh binding did not re-arm the hint; got %q", got)
	}
}
