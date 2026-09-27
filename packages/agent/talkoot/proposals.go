package talkoot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/privfs"
)

// ProposalsDir is the directory under a talkoot's directory that holds its
// proposals, one JSON file each.
//
// 🔑 The daemon names every file here from an id it mints, and no member
// names one. So the store needs no os.Root, as the notes do: a name cannot
// leave the directory, and a member's sandbox does not reach $TERVA_HOME.
const ProposalsDir = "proposals"

// MaxPendingProposals caps the proposals that wait for a person in one
// talkoot. A member that proposes on every turn fills the inbox otherwise.
const MaxPendingProposals = 50

// The states of a proposal.
const (
	ProposalPending  = "pending"
	ProposalApproved = "approved"
	ProposalDeclined = "declined"
)

// ErrProposalDamaged reports a proposal record that exists and does not
// read: it does not parse, names another id, or has no known status. A person
// can decline it, which writes a readable record over it.
var ErrProposalDamaged = errors.New("talkoot: the record of the proposal does not read")

// ErrProposalStale refuses an approval whose roster changed after the
// proposal was made (decision 0025, "a proposal can go stale").
var ErrProposalStale = errors.New("talkoot: the roster changed since this was proposed")

// Proposal is a batch of roster changes that waits for a person.
type Proposal struct {
	ID string `json:"id"`
	// Proposer is a member id, human: and a name for a person, or recruiter:
	// and a session id for a recruiter.
	Proposer string    `json:"proposer"`
	At       time.Time `json:"at"`
	// Base is RosterRevision of talkoot.md when the proposal was made. An
	// approval refuses when the roster has another revision.
	Base    string `json:"base"`
	Ops     []Op   `json:"ops"`
	Summary string `json:"summary"`
	// Why is the proposer's reason, as the card shows it.
	Why string `json:"why,omitempty"`
	// Undoes names the approved proposal this one reverses.
	Undoes string `json:"undoes,omitempty"`
	// Envelope is the room envelope that recorded the proposal.
	Envelope string `json:"envelope,omitempty"`
	// Changes previews each member before and after while the proposal
	// waits, and records what applied once it is approved.
	Changes   []MemberChange `json:"changes"`
	Status    string         `json:"status"`
	DecidedBy string         `json:"decided_by,omitempty"`
	DecidedAt time.Time      `json:"decided_at,omitzero"`
	Reason    string         `json:"reason,omitempty"`
	// Edited is set when the person approved changed operations, and Applied
	// holds them. Ops stays what the proposer asked for.
	Edited  bool `json:"edited,omitempty"`
	Applied []Op `json:"applied,omitempty"`
	// Problem says why an approval was refused. The proposal still waits.
	Problem string `json:"problem,omitempty"`
	// Persona is a new persona file that a recruiter drafted for the member
	// the operations add or edit. An approval writes it to the persona
	// library, byte for byte as the card showed it, before the roster
	// changes. Only a recruiter's proposal carries one.
	Persona *PersonaDraft `json:"persona,omitempty"`
}

// MaxPersonaDraftBytes bounds a drafted persona file. A built-in charter runs
// near 3 KiB.
const MaxPersonaDraftBytes = 16 * 1024

// PersonaDraft is a persona file that a proposal carries. Name is the
// reference the member entry uses, and Text is the whole file.
type PersonaDraft struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// RosterRevision names one text of talkoot.md.
func RosterRevision(text []byte) string {
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}

// NewProposalID mints a proposal id, which sorts by time.
func NewProposalID(now time.Time) string { return newID(now) }

var proposalIDPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ValidProposalID reports whether id can name a proposal. A caller that joins
// an id onto a path checks it first.
func ValidProposalID(id string) bool { return proposalIDPattern.MatchString(id) }

func proposalPath(dir, id string) (string, error) {
	if !ValidProposalID(id) {
		return "", fmt.Errorf("talkoot: %q is not a proposal id", id)
	}
	return filepath.Join(dir, ProposalsDir, id+".json"), nil
}

// SaveProposal writes a proposal, replacing the one with its id.
func SaveProposal(dir string, p Proposal) error {
	path, err := proposalPath(dir, p.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("talkoot: %w", err)
	}
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("talkoot: proposals: %w", err)
	}
	if err := privfs.WriteFile(path, raw); err != nil {
		return fmt.Errorf("talkoot: save proposal: %w", err)
	}
	return nil
}

// LoadProposal reads one proposal.
func LoadProposal(dir, id string) (Proposal, error) {
	path, err := proposalPath(dir, id)
	if err != nil {
		return Proposal{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Proposal{}, fmt.Errorf("talkoot: no proposal %s", id)
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("talkoot: read proposal: %w", err)
	}
	var p Proposal
	if err := json.Unmarshal(raw, &p); err != nil {
		return Proposal{}, fmt.Errorf("%w: %s: %v", ErrProposalDamaged, id, err)
	}
	// 🚨 A decision saves under p.ID. A record that names another id would
	// write a second file and leave this one waiting for good.
	if p.ID != id {
		return Proposal{}, fmt.Errorf("%w: %s names itself %q", ErrProposalDamaged, id, p.ID)
	}
	switch p.Status {
	case ProposalPending, ProposalApproved, ProposalDeclined:
	default:
		return Proposal{}, fmt.Errorf("%w: %s has the status %q", ErrProposalDamaged, id, p.Status)
	}
	return p, nil
}

// ListProposals returns every proposal in the talkoot at dir, oldest first.
// A file that does not read is left out of the list and named in damaged, so
// one damaged proposal does not hide the rest, and the caller can report it.
func ListProposals(dir string) (list []Proposal, damaged []string, err error) {
	entries, err := os.ReadDir(filepath.Join(dir, ProposalsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("talkoot: proposals: %w", err)
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidProposalID(id) || !e.Type().IsRegular() {
			continue
		}
		p, err := LoadProposal(dir, id)
		if err != nil {
			damaged = append(damaged, id)
			continue
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, damaged, nil
}
