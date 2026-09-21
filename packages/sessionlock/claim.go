package sessionlock

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ClaimFormat is the version of the record this binary writes. A reader that
// meets a higher number keeps what it can parse rather than discarding the
// claim: see Manager.refuseByRecord, which branches on kind and expiry and
// never on the format number.
const ClaimFormat = 1

// Kind separates a lock a running terva takes for itself from one a person
// made deliberately. The difference is not cosmetic. An automatic lock is
// backed by a live process and dies with it. An explicit claim has no process
// at all, so pid liveness cannot answer anything about it, and only the expiry
// can end it.
type Kind string

const (
	// KindAuto is the lock a terva takes while it writes to a session.
	KindAuto Kind = "auto"
	// KindExplicit is a claim a person made, which outlives the process.
	KindExplicit Kind = "explicit"
)

// Claim is the payload beside the flock: everything a second process needs to
// explain a refusal, and everything needed to decide whether the holder is
// still there.
type Claim struct {
	Format  int    `json:"format"`
	Session string `json:"session"` // the transcript's filename stem
	Kind    Kind   `json:"kind"`
	Reason  string `json:"reason"`         // why this session is held, for a person
	Holder  string `json:"holder"`         // "terva web", "terva --print"
	Host    string `json:"host,omitempty"` // a pid means nothing off the host that made it
	PID     int    `json:"pid,omitempty"`  // 0 for an explicit claim: there is no process
	Version string `json:"terva_version,omitempty"`

	ClaimedAt string `json:"claimed_at"`
	ExpiresAt string `json:"expires_at"`
	Heartbeat string `json:"heartbeat,omitempty"`

	// BeatSeconds is the interval the WRITER restamps on, so a reader works out
	// this holder's grace rather than applying its own. A future terva that
	// beats more slowly is then not called crashed by today's binary.
	// config.listenGrace is a compile-time constant on the reader's side and
	// cannot do this.
	BeatSeconds int `json:"heartbeat_interval_seconds,omitempty"`
}

// ClaimPath is the record file for a transcript. It is the ONLY file this
// package adds, and it exists only while a session is held.
//
// It does not end in .jsonl, which is what keeps it invisible to
// isSessionTranscriptName and so to every bucket scanner in packages/core.
func ClaimPath(transcriptPath string) string {
	return strings.TrimSuffix(transcriptPath, ".jsonl") + ".lock.json"
}

// ArtifactPaths is every file this package may leave beside a transcript, for a
// caller that deletes or archives the session and has to take them along.
func ArtifactPaths(transcriptPath string) []string {
	return []string{ClaimPath(transcriptPath)}
}

// parseTime reads an RFC 3339 stamp, reporting whether it was readable at all.
// Callers must distinguish "absent or corrupt" from "in the past": treating an
// unreadable expiry as expired would hand a live session away on a typo.
func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// expiredAt reports whether the claim has lapsed. An unreadable or absent
// expiry is NOT expired: the record is then evidence of a holder whose end we
// cannot date, and the flock stays the thing that decides.
func (c Claim) expiredAt(now time.Time) bool {
	exp, ok := parseTime(c.ExpiresAt)
	if !ok {
		return false
	}
	return now.After(exp)
}

// beatFreshAt reports whether the heartbeat still looks live.
//
// A heartbeat from the future is fresh, not broken: a clock that disagrees is
// not evidence that a process died, and packages/agent/workflow/runs settled
// the same question the same way. A record with no heartbeat at all is not
// fresh, which is right for an automatic lock and irrelevant to an explicit
// claim, since nothing consults this for one.
func (c Claim) beatFreshAt(now time.Time, fallbackBeat time.Duration) bool {
	beat, ok := parseTime(c.Heartbeat)
	if !ok {
		return false
	}
	if beat.After(now) {
		return true
	}
	interval := fallbackBeat
	if c.BeatSeconds > 0 {
		interval = time.Duration(c.BeatSeconds) * time.Second
	}
	return now.Sub(beat) <= 3*interval
}

// Describe renders the claim as one sentence for a person. It is deliberately
// plain English with no i18n dependency, because this package is a leaf;
// packages/agent wraps it for display.
func (c Claim) Describe() string {
	var b strings.Builder
	holder := c.Holder
	if holder == "" {
		holder = "another terva"
	}
	b.WriteString(holder)
	if c.PID > 0 {
		fmt.Fprintf(&b, " (pid %d", c.PID)
		if c.Host != "" {
			fmt.Fprintf(&b, " on %s", c.Host)
		}
		b.WriteString(")")
	}
	if c.Kind == KindExplicit {
		b.WriteString(" holds an explicit claim on this session")
	} else {
		b.WriteString(" is writing to this session")
	}
	if c.Reason != "" {
		fmt.Fprintf(&b, ", because %s", strings.TrimSuffix(c.Reason, "."))
	}
	b.WriteString(".")
	if exp, ok := parseTime(c.ExpiresAt); ok {
		fmt.Fprintf(&b, " The claim lapses at %s.", exp.UTC().Format(time.RFC3339))
	}
	return b.String()
}

// readClaim loads the record beside a transcript. A missing file and an
// unparseable one both come back as "no claim", because neither is evidence
// about a holder and the caller's next question is the flock either way.
func readClaim(path string) (Claim, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Claim{}, false
	}
	var c Claim
	if err := json.Unmarshal(b, &c); err != nil {
		return Claim{}, false
	}
	return c, true
}
