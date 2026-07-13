package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Title provenance round-trips through the session file: generated rename
// rows carry the source marker, user rows don't, and both OpenSession and
// DescribeSessions surface the LAST row's verdict. Legacy source-less rows
// (every rename written before provenance existed) read as manual — the
// conservative default, so automatic re-titling never clobbers them.
func TestTitleProvenanceRoundTrip(t *testing.T) {
	tmp := testsupport.TempDir(t)
	s, err := NewSession(tmp, tmp, "p", "m", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = s.AppendMessage(provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}}})
	path := s.Path
	s.Close()

	check := func(wantTitle string, wantGenerated bool) {
		t.Helper()
		re, _, err := OpenSession(path)
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		re.Close()
		if re.Meta.Title != wantTitle || re.TitleGenerated != wantGenerated {
			t.Fatalf("OpenSession: title=%q generated=%v, want %q/%v",
				re.Meta.Title, re.TitleGenerated, wantTitle, wantGenerated)
		}
		sum := describeSession(path)
		if sum.Title != wantTitle || sum.TitleGenerated != wantGenerated {
			t.Fatalf("describeSession: title=%q generated=%v, want %q/%v",
				sum.Title, sum.TitleGenerated, wantTitle, wantGenerated)
		}
	}

	if err := RenameSessionGenerated(path, "machine name"); err != nil {
		t.Fatalf("RenameSessionGenerated: %v", err)
	}
	check("machine name", true)

	// A user rename after a generated one: the LAST row wins, provenance
	// flips to manual.
	if err := RenameSession(path, "my name"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	check("my name", false)

	// And back: an explicit generate over a manual rename is allowed
	// (the verb's contract) and re-marks the title machine-owned.
	if err := RenameSessionGenerated(path, "regenerated"); err != nil {
		t.Fatalf("RenameSessionGenerated: %v", err)
	}
	check("regenerated", true)
}
