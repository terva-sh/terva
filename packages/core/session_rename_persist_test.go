package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// A rename is an explicit naming act. Any meta row written afterwards used to
// revert it on the next load, because the meta fold assigned Title
// unconditionally and no rename path writes the new name back into the live
// Session.Meta — RenameSession is a path-based append with no live session to
// update, and the workspace's setTitle only touches wsSession state. So the next
// writeMeta emitted whatever Title the session started with, usually "".
//
// The triggers are ordinary session activity, not edge cases: /model
// (UpdateModel), /note (SetNote), a background change, StampVersion on an
// upgraded resume, and bumpFormatForAmend on the FIRST edit or retry.
//
// TestTitleProvenanceRoundTrip only interleaved rename rows with each other, so
// it could not see this. These interleave a rename with a META row, which is the
// combination that broke.
func TestRenameSurvivesALaterMetaRow(t *testing.T) {
	s := mvNewSession(t, mvMsg(provider.RoleUser, "u0"))
	path := s.Path
	if err := s.UpdateModel("openai", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	if err := RenameSession(path, "my name"); err != nil {
		t.Fatal(err)
	}

	// Reopen and do something utterly ordinary that writes a meta row.
	s2, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.UpdateModel("anthropic", "claude-sonnet-4.5"); err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()

	// Both readers must still report the user's name. They are separate folds
	// with byte-identical logic, so they agreed on the wrong answer before —
	// which is the worst way for two readers to agree.
	s3, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s3.Meta.Title; got != "my name" {
		t.Errorf("OpenSession title = %q, want %q — a meta row written after the rename reverted it", got, "my name")
	}
	if s3.Meta.Model != "claude-sonnet-4.5" {
		t.Errorf("the meta row's own fields must still apply: model = %q", s3.Meta.Model)
	}
	_ = s3.Close()

	sum := describeSession(path)
	if sum.Title != "my name" {
		t.Errorf("DescribeSession title = %q, want %q", sum.Title, "my name")
	}
	if sum.Model != "claude-sonnet-4.5" {
		t.Errorf("DescribeSession model = %q, want the value the later meta row set", sum.Model)
	}
}

// A rename after a meta row must still win — the ordering that already worked,
// kept honest so the fix above cannot be read as "meta never sets a title".
func TestRenameAfterAMetaRowStillWins(t *testing.T) {
	s := mvNewSession(t, mvMsg(provider.RoleUser, "u0"))
	path := s.Path
	if err := s.UpdateModel("openai", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := RenameSession(path, "later name"); err != nil {
		t.Fatal(err)
	}
	if got := describeSession(path).Title; got != "later name" {
		t.Errorf("title = %q, want %q", got, "later name")
	}
}
