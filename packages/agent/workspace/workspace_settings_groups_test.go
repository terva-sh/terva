package workspace

import (
	"context"
	"slices"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The settings-groups guard (docs/proposals/settings-groups.md): every item
// the pane emits names a declared group, and the flat Items array reads
// group-by-group in the declared order, so a groups-blind client renders a
// sensibly ordered list and a groups-aware one partitions without a trailing
// "other" bucket. Every conditional item is flipped visible first — a
// forgotten Group on a conditional would otherwise hide from this test the
// way it hides from a default-config session.
func TestSettingsItemsAllNameDeclaredGroups(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	s := &wsSession{id: "s1", ws: w, hub: newWSHub()}
	w.sessions[s.id] = s
	s.agent = core.NewAgent(&gatedTurnClient{}, "m", "sys", core.Registry{})

	on := true
	if err := config.MutateConfig(func(c *config.Config) {
		c.LazyTools = &on
		c.AutoSwarmEnabled = &on
		c.Raati.ConveneTool = true
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	v := s.settingsView()

	if len(v.Groups) != len(settingGroups) {
		t.Fatalf("view carries %d groups; the declaration has %d", len(v.Groups), len(settingGroups))
	}
	rank := map[string]int{}
	for i, g := range v.Groups {
		if g.ID != settingGroups[i].ID {
			t.Errorf("group %d = %q; want %q (wire order must match the declaration)", i, g.ID, settingGroups[i].ID)
		}
		if _, dup := rank[g.ID]; dup {
			t.Errorf("group id %q declared twice", g.ID)
		}
		if g.Label == "" {
			t.Errorf("group %q has no label", g.ID)
		}
		if g.Desc == "" {
			t.Errorf("group %q has no description for the category picker", g.ID)
		}
		rank[g.ID] = i
	}

	// Every item names a declared group, and the flat order is group-by-group:
	// the group rank never decreases as the list is walked.
	last := 0
	for i, it := range v.Items {
		r, ok := rank[it.Group]
		if !ok {
			t.Errorf("item %q names group %q, which no SettingGroup declares", it.Key, it.Group)
			continue
		}
		if r < last {
			t.Errorf("item %d (%q, group %q) breaks the group-by-group order", i, it.Key, it.Group)
		}
		last = r
	}

	// The conditional nesting survives inside its group: each child sits
	// directly after its parent, not wherever construction happened to put it.
	keys := make([]string, len(v.Items))
	for i, it := range v.Items {
		keys[i] = it.Key
	}
	for parent, children := range map[string][]string{
		"auto_swarm":    {"auto_swarm_nudge", "external_workers"},
		"lazy_tools":    {"activation_continuation"},
		"raati_convene": {"raati_spare_host"},
	} {
		p := slices.Index(keys, parent)
		if p < 0 {
			t.Fatalf("parent %q missing from the pane", parent)
		}
		for off, child := range children {
			want := p + 1 + off
			if want >= len(keys) || keys[want] != child {
				t.Errorf("%q must sit directly under %q; the list runs %v around it", child, parent, keys[max(0, p):min(len(keys), p+4)])
			}
		}
	}

	// Every engine feature declares a group the pane knows, so a new feature
	// cannot land in the trailing bucket a groups-aware client renders as
	// "other".
	for _, f := range build.EngineFeatures {
		if _, ok := rank[f.Group]; !ok {
			t.Errorf("engine feature %q declares group %q, which no SettingGroup declares", f.ID, f.Group)
		}
	}
}
