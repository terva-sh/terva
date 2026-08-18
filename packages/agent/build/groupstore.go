package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/slug"

	"terva.sh/terva/packages/privfs"
)

// A Group is a lightweight, terva-owned membership bucket — a way to organize
// the library for browsing, DISTINCT from two neighbours it is easy to confuse
// with:
//
//   - a card's CCv2 tags are author labels living INSIDE the card JSON, edited
//     only by rewriting the whole card; a group is terva's own metadata, kept
//     outside the member, so assigning it never touches the card.
//   - a World is a heavy session-seeding object (roster + lore + coordination);
//     a group carries no play semantics at all, just a name and a member list.
//
// A card or session may belong to any number of groups. Membership lives HERE,
// in the group, not on the member — so a card/session is never rewritten to be
// filed, and deleting one never has to reach into every group (a stale member
// is simply filtered out on read by the caller that knows the live set).
//
// Two independent namespaces share this one store, differing only in their root
// directory and what a member string means:
//
//	$TERVA_HOME/card-groups/<id>.json     — members are card refs
//	$TERVA_HOME/session-groups/<id>.json  — members are session ids
//
// A flat file per group (the userpersona pattern, not the worlds dir-per-doc
// one) because a group carries no sidecar assets.

// Group is one saved bucket. The id is minted at creation (slug + short hash)
// and STABLE across renames — the name is just a label, so a group mutates in
// place and its id must not follow its contents.
type Group struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
	// Members are card refs (card-groups) or session ids (session-groups). The
	// store never validates them against the live library; a member deleted
	// since it was filed is dropped by the caller on read.
	Members []string  `json:"members"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// CardGroupsDir / SessionGroupsDir are the on-disk roots for the two namespaces.
func CardGroupsDir() string    { return filepath.Join(config.TervaHome(), "card-groups") }
func SessionGroupsDir() string { return filepath.Join(config.TervaHome(), "session-groups") }

// GroupStore is a flat library of groups rooted at one directory. Created lazily
// on first save; a missing directory reads as an empty library.
type GroupStore struct{ dir string }

// NewCardGroupStore / NewSessionGroupStore open the two namespaces at the
// current $TERVA_HOME.
func NewCardGroupStore() *GroupStore    { return &GroupStore{dir: CardGroupsDir()} }
func NewSessionGroupStore() *GroupStore { return &GroupStore{dir: SessionGroupsDir()} }

// List returns every group, sorted by name then id. A file that fails to parse
// is skipped rather than failing the listing.
func (s *GroupStore) List() ([]Group, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Group
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if doc, err := s.Get(id); err == nil {
			out = append(out, doc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get loads one group by id.
func (s *GroupStore) Get(id string) (Group, error) {
	id = strings.TrimSpace(id)
	if err := slug.ValidID(id); err != nil {
		return Group{}, fmt.Errorf("invalid group id %q", id)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return Group{}, fmt.Errorf("group %q: %w", id, err)
	}
	var doc Group
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Group{}, fmt.Errorf("group %q: %w", id, err)
	}
	doc.ID = id // the filename is authoritative
	return doc, nil
}

// Save writes a group. An empty doc.ID mints one (slug + short hash — stable
// thereafter) and stamps Created; a non-empty ID updates in place, preserving
// Created. Updated is stamped either way. Members are written exactly as given,
// deduplicated with order preserved. Returns the stored doc.
func (s *GroupStore) Save(doc Group) (Group, error) {
	if strings.TrimSpace(doc.Name) == "" {
		return Group{}, fmt.Errorf("a group needs a name")
	}
	now := time.Now().UTC()
	if doc.ID == "" {
		doc.ID = groupID(doc.Name, now)
		doc.Created = now
	} else {
		if prev, err := s.Get(doc.ID); err == nil {
			doc.Created = prev.Created
		} else if doc.Created.IsZero() {
			doc.Created = now
		}
	}
	doc.Updated = now
	doc.Members = dedupe(doc.Members)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Group{}, err
	}
	if err := privfs.MkdirAll(s.dir); err != nil {
		return Group{}, err
	}
	if err := privfs.WriteFileMode(filepath.Join(s.dir, doc.ID+".json"), raw, 0o644); err != nil {
		return Group{}, err
	}
	return doc, nil
}

// Delete removes a group. Members keep every other group they belong to — a
// group is a view, never a container that owns its cards or sessions.
func (s *GroupStore) Delete(id string) error {
	id = strings.TrimSpace(id)
	if err := slug.ValidID(id); err != nil {
		return fmt.Errorf("invalid group id %q", id)
	}
	p := filepath.Join(s.dir, id+".json")
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("group %q: %w", id, err)
	}
	return os.Remove(p)
}

// dedupe returns members with blanks dropped and duplicates removed, keeping
// first-seen order.
func dedupe(members []string) []string {
	seen := make(map[string]bool, len(members))
	out := make([]string, 0, len(members))
	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// groupID mints a stable, human-legible id: the name's slug plus a short hash of
// name+moment (like worldID, and unlike cardID's content hash — a group mutates
// in place, so its id must not follow its contents).
func groupID(name string, now time.Time) string {
	sum := sha256.Sum256([]byte(name + now.Format(time.RFC3339Nano)))
	h := hex.EncodeToString(sum[:])[:8]
	stem := slug.Of(name)
	if stem == "" {
		return h
	}
	return stem + "-" + h
}
