package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

func userMessage(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func assistantMessage(text string, meta map[string]string) provider.Message {
	return provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Meta:    meta,
	}
}

// TestResumeStateOf is the whole contract. The cases that earn their keep are the
// RoleUser ones: they all look identical to a role check, and two of them mean
// the session is healthy while three mean it is stranded.
func TestResumeStateOf(t *testing.T) {
	for _, tc := range []struct {
		what string
		msgs []provider.Message
		want ResumeState
	}{
		{
			"an empty transcript",
			nil,
			ResumeNotStuck,
		},
		{
			"a finished reply",
			[]provider.Message{userMessage("hello"), assistantMessage("hi there", nil)},
			ResumeNotStuck,
		},
		{
			"a prompt that never got an answer",
			[]provider.Message{userMessage("hello")},
			ResumeAfterUser,
		},
		{
			"a reply the provider cut short",
			[]provider.Message{
				userMessage("hello"),
				assistantMessage("I was saying", map[string]string{MetaIncomplete: "true"}),
			},
			ResumeAfterCutShort,
		},
		{
			"tool results the model never read",
			[]provider.Message{
				userMessage("read the file"),
				assistantMessage("calling read", nil),
				{Role: provider.RoleTool, Content: []provider.Content{provider.TextBlock{Text: "file body"}}},
			},
			ResumeAfterTools,
		},
		{
			"a tool-image mirror, which is a tool round wearing RoleUser",
			[]provider.Message{
				userMessage("look at this"),
				{Role: provider.RoleUser, Meta: map[string]string{toolImageMirrorMeta: "true"}},
			},
			ResumeAfterTools,
		},
		{
			"a compaction summary, which leaves the session idle and healthy",
			[]provider.Message{
				{Role: provider.RoleUser, Meta: map[string]string{MetaCompaction: "true"}},
			},
			ResumeNotStuck,
		},
		{
			"a clear divider",
			[]provider.Message{
				assistantMessage("done", nil),
				{Role: provider.RoleUser, Meta: map[string]string{MetaClear: "true"}},
			},
			ResumeNotStuck,
		},
		{
			"a crossed clear divider, which carries a different value",
			[]provider.Message{
				assistantMessage("done", nil),
				{Role: provider.RoleUser, Meta: map[string]string{MetaClear: "crossed"}},
			},
			ResumeNotStuck,
		},
		{
			"a host-injected nudge, written to drive a turn that never ran",
			[]provider.Message{
				assistantMessage("done", nil),
				{
					Role:    provider.RoleUser,
					Content: []provider.Content{provider.TextBlock{Text: "keep going"}},
					Meta:    map[string]string{MetaSynthetic: "true"},
				},
			},
			ResumeAfterUser,
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			got := ResumeStateOf(tc.msgs)
			if got != tc.want {
				t.Errorf("ResumeStateOf(%s) = %s; want %s", tc.what, got, tc.want)
			}
			if got.Stuck() != (tc.want != ResumeNotStuck) {
				t.Errorf("Stuck() = %v for state %s; the two disagree", got.Stuck(), got)
			}
		})
	}
}

// TestResumeStateIgnoresEarlierMessages: only the tail decides. A session that
// recovered from an earlier failure carries the old incomplete mark forever, and
// reading the whole transcript would keep offering to resume work that is done.
func TestResumeStateIgnoresEarlierMessages(t *testing.T) {
	msgs := []provider.Message{
		userMessage("first"),
		assistantMessage("cut off here", map[string]string{MetaIncomplete: "true"}),
		userMessage("second"),
		assistantMessage("a complete answer", nil),
	}
	if got := ResumeStateOf(msgs); got != ResumeNotStuck {
		t.Errorf("ResumeStateOf = %s; want not-stuck — an old incomplete reply is history, not a live problem", got)
	}
}

// TestResumeAfterCutShortNeedsTheMark is the coupling to MetaIncomplete. Without
// the mark a cut-short reply is indistinguishable from a finished one, which is
// the reason the mark exists at all.
func TestResumeAfterCutShortNeedsTheMark(t *testing.T) {
	unmarked := []provider.Message{userMessage("hello"), assistantMessage("I was saying", nil)}
	if got := ResumeStateOf(unmarked); got != ResumeNotStuck {
		t.Errorf("ResumeStateOf = %s; an unmarked assistant message is the ordinary idle state", got)
	}
	marked := []provider.Message{userMessage("hello"), assistantMessage("I was saying", map[string]string{MetaIncomplete: "true"})}
	if got := ResumeStateOf(marked); got != ResumeAfterCutShort {
		t.Errorf("ResumeStateOf = %s; want after-cut-short once the mark is on", got)
	}
}
