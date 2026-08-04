package secretadmin

import (
	"fmt"
	"io"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// WriteStatus renders a [ctrlproto.SecretsStatus] for a terminal.
//
// The CLI goes through the struct rather than reading disk a second time. That
// is what makes "the panel and the terminal agree" a structural property
// instead of a promise: there is one producer, and this is a view of it.
func WriteStatus(w io.Writer, st ctrlproto.SecretsStatus) {
	writeKey(w, st.Key)
	writeComponents(w, st.Components)
	if st.RetiredKeys > 0 {
		line(w, "retired keys", fmt.Sprintf("%d in %s — they OPEN files not yet rewritten, and never seal (`terva secret rotate --revoke` destroys them)",
			st.RetiredKeys, config.RetiredSecretsKeyPath()))
	}
	if st.Recipient != "" {
		line(w, "recipient", st.Recipient)
	} else {
		line(w, "recipient", "not recorded in config.json (`terva secret migrate` records it)")
	}
	for _, f := range st.Files {
		// The note completes the sentence the state starts ("PLAINTEXT bot token
		// — run ..."), rather than being appended to it: a separator here made
		// the row read "PLAINTEXT — holds a plaintext bot token".
		if f.Note != "" {
			line(w, f.Name, strings.ToUpper(f.State)+" "+f.Note)
			continue
		}
		line(w, f.Name, f.State)
	}
	writeStore(w, st.Store)
	// Only once a store exists. Grants live INSIDE secrets.json, so on a home
	// that has none "no grants" restates the line above it in different words —
	// but from the moment the store is there the section must always render,
	// empty or not, because one that vanishes when empty reads as missing.
	if st.Store.Present {
		writeGrants(w, st.Grants)
	}
	writeConfig(w, st.Config)
	writeReads(w, st.Reads)
}

func writeKey(w io.Writer, k ctrlproto.SecretsKey) {
	switch k.State {
	case ctrlproto.SecretsKeyPresent:
		switch {
		case k.FromEnv:
			line(w, "key", k.Path+"  (supplied via environment)")
		case k.OwnerOnly:
			line(w, "key", fmt.Sprintf("%s  %s  owner-only", k.Path, k.Mode))
		case k.Mode == "":
			// A platform that reports no usable mode (Windows) still has a
			// reason worth printing; an empty column between them would read
			// as a missing value rather than an absent concept.
			line(w, "key", fmt.Sprintf("%s  %s", k.Path, k.Reason))
		default:
			line(w, "key", fmt.Sprintf("%s  %s  %s", k.Path, k.Mode, k.Reason))
		}
	case ctrlproto.SecretsKeyMissing:
		line(w, "key", fmt.Sprintf("MISSING from %s — %s", k.Path, k.Reason))
	case ctrlproto.SecretsKeyUnusable:
		line(w, "key", "UNUSABLE: "+k.Reason)
	default:
		line(w, "key", "absent — "+k.Reason)
	}
}

func writeComponents(w io.Writer, cs []ctrlproto.SecretsComponent) {
	for _, c := range cs {
		if !c.Registered {
			line(w, "component", fmt.Sprintf("%s holds sealed values but has NOT registered a recipient — a rotation cannot re-seal it; start it once to register", c.File))
			continue
		}
		seen := "never seen"
		if c.LastSeen != nil {
			seen = "last seen " + c.LastSeen.Format(time.RFC3339)
		}
		line(w, "component", fmt.Sprintf("%s — %d sealed path(s), %s", c.Scope, c.Paths, seen))
	}
}

// writeReads prints one row per component directory that is NOT plainly
// readable. A clean component says nothing: the readable state is the norm and
// the whole goal, and a row per healthy component would bury the ones that
// need action.
func writeReads(w io.Writer, reads []ctrlproto.SecretsComponentRead) {
	for _, r := range reads {
		if r.Readable {
			continue
		}
		verb := "DENIED —"
		if !r.Enforced {
			verb = "will be denied in a future release —"
		}
		// The same label config.json's verdict uses, and printed beside it: both
		// answer "what may the agent read?", and two labels for one question
		// reads as two unrelated features.
		line(w, "agent reads", fmt.Sprintf("%s %s %s", r.Scope, verb, r.Reason))
	}
}

func writeStore(w io.Writer, s ctrlproto.SecretsStore) {
	switch {
	case s.Error != "":
		line(w, "secrets.json", "UNREADABLE: "+s.Error)
	case !s.Present:
		line(w, "secrets.json", "absent — no scoped secrets stored")
	default:
		state := "PLAINTEXT"
		if s.Encrypted {
			state = "encrypted"
		}
		var parts []string
		for _, sc := range s.Scopes {
			parts = append(parts, fmt.Sprintf("%s (%d)", sc.Scope, sc.Keys))
		}
		line(w, "secrets.json", fmt.Sprintf("%s — %s", state, strings.Join(parts, ", ")))
	}
}

// writeGrants prints "none" rather than nothing. A section that vanishes when
// empty reads as missing, and "no grants" is the fact a reader came for.
func writeGrants(w io.Writer, gs []ctrlproto.SecretsGrant) {
	if len(gs) == 0 {
		line(w, "grants", "none — every scope is reachable only by its own principal")
		return
	}
	for _, g := range gs {
		note := ""
		if g.Expires != nil {
			note = " until " + g.Expires.Format(time.RFC3339)
			if g.Expired {
				note += " (EXPIRED)"
			}
		}
		line(w, "grant", fmt.Sprintf("%s -> %s (%s)%s", g.Principal, g.Scope, g.Mode, note))
	}
}

func writeConfig(w io.Writer, c ctrlproto.SecretsConfigScan) {
	switch {
	case c.Total == 0:
		line(w, "config.json", "no secret-bearing values")
	case len(c.Plaintext) == 0:
		line(w, "config.json", fmt.Sprintf("%d secret value(s), all encrypted", c.Total))
	default:
		line(w, "config.json", fmt.Sprintf("%d of %d secret value(s) still PLAINTEXT — run `terva secret migrate`", len(c.Plaintext), c.Total))
		for _, p := range c.Plaintext {
			line(w, "", p)
		}
	}
	if len(c.Unclassified) > 0 {
		line(w, "", "unclassifiable (no manifest): "+strings.Join(c.Unclassified, ", "))
	}
	// What the encryption actually BUYS, stated plainly: with nothing secret
	// left in the file, the agent's own tools may read it.
	if c.AgentCanRead {
		line(w, "agent reads", "config.json readable (nothing secret left in it)")
	} else {
		line(w, "agent reads", "config.json denied — "+c.Reason)
	}
}

// WriteList renders the store's scopes and key names.
func WriteList(w io.Writer, res ctrlproto.SecretsListResult) {
	if len(res.Scopes) == 0 {
		fmt.Fprintln(w, "no scoped secrets stored")
		return
	}
	for _, sc := range res.Scopes {
		fmt.Fprintf(w, "%s\n", sc.Scope)
		for _, k := range sc.Keys {
			fmt.Fprintf(w, "  %s\n", k)
		}
	}
}

// WriteForget reports what a forget actually removed. "terva knew nothing about
// this scope" must not render the same as a forget that worked — a silent
// success on a typo'd scope is how an operator concludes a rotation is unblocked
// when it is not.
func WriteForget(w io.Writer, scope string, res ctrlproto.SecretsForgetResult) {
	var did []string
	if res.Component {
		did = append(did, "removed the registry entry")
	}
	if res.Grants > 0 {
		did = append(did, fmt.Sprintf("dropped %d grant(s)", res.Grants))
	}
	if res.Values > 0 {
		did = append(did, fmt.Sprintf("deleted %d stored value(s)", res.Values))
	}
	if len(did) == 0 {
		fmt.Fprintf(w, "%s: nothing to forget — no registry entry, no grants, no stored values\n", scope)
		return
	}
	fmt.Fprintf(w, "%s: %s\n", scope, strings.Join(did, ", "))
	if res.Remaining > 0 {
		fmt.Fprintf(w, "%s: left %d stored value(s) in place — re-run with --purge to delete them too\n", scope, res.Remaining)
	}
}

func line(w io.Writer, label, value string) {
	fmt.Fprintf(w, "  %-12s %s\n", label, value)
}
