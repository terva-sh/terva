package talkoot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// A record that parses but names another id, or carries no known status, is
// damaged. A decision would otherwise save it under another name, or refuse
// it as decided.
func TestARecordThatDoesNotMatchItsFileIsDamaged(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ProposalsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	good := NewProposalID(now)
	other := NewProposalID(now.Add(time.Second))
	for _, c := range []struct{ name, body string }{
		{"another id", `{"id":"` + other + `","status":"pending"}`},
		{"no status", `{"id":"` + good + `"}`},
		{"an unknown status", `{"id":"` + good + `","status":"held"}`},
	} {
		if err := os.WriteFile(filepath.Join(dir, ProposalsDir, good+".json"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProposal(dir, good); !errors.Is(err, ErrProposalDamaged) {
			t.Errorf("%s: want ErrProposalDamaged, got %v", c.name, err)
		}
		if list, damaged, err := ListProposals(dir); err != nil || len(list) != 0 || len(damaged) != 1 || damaged[0] != good {
			t.Errorf("%s: the listing is %+v, damaged %v, %v", c.name, list, damaged, err)
		}
	}
	if err := SaveProposal(dir, Proposal{ID: good, Status: ProposalPending}); err != nil {
		t.Fatal(err)
	}
	if p, err := LoadProposal(dir, good); err != nil || p.ID != good {
		t.Fatalf("a record that matches its file: %+v, %v", p, err)
	}
}
