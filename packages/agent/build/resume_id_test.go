package build

import "testing"

// --resume's optional value is consumed ONLY when it has the session-id
// shape, so a positional prompt (or another flag) after -r is never
// swallowed (docs/proposals/resume-picker.md stage 4).
func TestParseArgsResumeOptionalID(t *testing.T) {
	cases := []struct {
		name   string
		in     []string
		id     string
		prompt string
	}{
		{"bare", []string{"-r"}, "", ""},
		{"id", []string{"-r", "20260712-174955-2ba5b031"}, "20260712-174955-2ba5b031", ""},
		{"id with extension", []string{"--resume", "20260712-174955-2ba5b031.jsonl"}, "20260712-174955-2ba5b031.jsonl", ""},
		{"prompt not swallowed", []string{"-r", "fix the failing test"}, "", "fix the failing test"},
		{"flag not swallowed", []string{"-r", "--no-tools"}, "", ""},
		{"id then prompt", []string{"-r", "20260712-174955-2ba5b031", "hello"}, "20260712-174955-2ba5b031", "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := ParseArgs(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !a.Resume {
				t.Fatal("Resume flag not set")
			}
			if a.ResumeID != tc.id {
				t.Fatalf("ResumeID = %q, want %q", a.ResumeID, tc.id)
			}
			if a.Prompt != tc.prompt {
				t.Fatalf("Prompt = %q, want %q", a.Prompt, tc.prompt)
			}
		})
	}
}

func TestLooksLikeSessionID(t *testing.T) {
	yes := []string{
		"20260712-174955-2ba5b031",
		"20260712-174955-2ba5b031.jsonl",
	}
	no := []string{
		"",
		"fix the failing test",
		"20260712-174955-2BA5B031",  // uppercase hex is never generated
		"2026712-174955-2ba5b031",   // short date
		"20260712-174955-2ba5b03",   // short hash
		"20260712_174955_2ba5b031",  // wrong separators
		"20260712-174955-2ba5b0311", // long
		"session.jsonl",
	}
	for _, s := range yes {
		if !LooksLikeSessionID(s) {
			t.Errorf("LooksLikeSessionID(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if LooksLikeSessionID(s) {
			t.Errorf("LooksLikeSessionID(%q) = true, want false", s)
		}
	}
}
