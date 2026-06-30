package agent

import (
	"reflect"
	"testing"
	"time"
)

func TestExtractIdleNudgeFlags(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantRest   []string
		wantAfter  time.Duration
		wantPrompt string
	}{
		{"none", []string{"--model", "opus", "-c"}, []string{"--model", "opus", "-c"}, 0, ""},
		{"space form", []string{"--idle-nudge", "30m"}, nil, 30 * time.Minute, ""},
		{"equals form", []string{"--idle-nudge=45s"}, nil, 45 * time.Second, ""},
		{"with prompt", []string{"--idle-nudge", "1m", "--idle-prompt", "say hi"}, nil, time.Minute, "say hi"},
		{"malformed dur ignored", []string{"--idle-nudge", "bogus"}, nil, 0, ""},
		{"preserves surrounding args", []string{"--model", "x", "--idle-nudge", "2m", "-c"}, []string{"--model", "x", "-c"}, 2 * time.Minute, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rest, after, prompt := extractIdleNudgeFlags(c.in)
			if after != c.wantAfter {
				t.Errorf("idleAfter = %v, want %v", after, c.wantAfter)
			}
			if prompt != c.wantPrompt {
				t.Errorf("idlePrompt = %q, want %q", prompt, c.wantPrompt)
			}
			if !reflect.DeepEqual(rest, c.wantRest) {
				t.Errorf("rest = %#v, want %#v", rest, c.wantRest)
			}
		})
	}
}
