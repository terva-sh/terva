package build

import (
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func TestManifestRenderers(t *testing.T) {
	m := PromptManifest{Sections: []PromptSection{
		{Name: "system", Segments: []PromptSegment{{Source: "identity-intro", Text: "You are X."}}},
		{Name: "messages", Segments: []PromptSegment{{Source: "card:greeting", Role: "assistant", Text: "Hi."}}},
	}}

	txt := m.Text()
	// A system segment carries its portability class; a message does not — the
	// table is not about the conversation (see PromptSection.Classified).
	if !strings.Contains(txt, "==== SYSTEM ====") || !strings.Contains(txt, "[identity-intro · portable]") || !strings.Contains(txt, "assistant · card:greeting") {
		t.Errorf("annotated text missing labels: %q", txt)
	}

	raw := m.Raw()
	if !strings.Contains(raw, "You are X.") || strings.Contains(raw, "[identity-intro]") {
		t.Errorf("raw should carry text but no labels: %q", raw)
	}

	var back struct {
		Sections []PromptSection `json:"sections"`
	}
	if err := json.Unmarshal([]byte(m.JSON()), &back); err != nil {
		t.Fatalf("json invalid: %v", err)
	}
	if len(back.Sections) != 2 || back.Sections[1].Segments[0].Source != "card:greeting" {
		t.Errorf("json round-trip lost structure: %+v", back)
	}
}

func TestMessageSegments(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Greetings."}}, Meta: map[string]string{"source": "card:greeting"}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
	}
	segs := messageSegments(msgs)
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %d", len(segs))
	}
	if segs[0].Source != "card:greeting" || segs[0].Role != "assistant" {
		t.Errorf("greeting label wrong: %+v", segs[0])
	}
	if segs[1].Source != "user" || segs[1].Text != "hi" {
		t.Errorf("user segment wrong: %+v", segs[1])
	}
}

func TestLoreFiredLabel(t *testing.T) {
	l := loreFiredLabel([]lore.Entry{{Source: "vault.md"}, {Source: "secret.md"}})
	if !strings.Contains(l, "vault.md") || !strings.Contains(l, "secret.md") {
		t.Errorf("fired label = %q", l)
	}
	if loreFiredLabel(nil) == "" {
		t.Error("nil entries should still yield a label")
	}
}

func TestToolManifestSegments_Empty(t *testing.T) {
	if toolManifestSegments(core.Registry{}, nil) != nil {
		t.Error("empty registry -> nil segments")
	}
}

// The offline dump runs before injectHostTools, so the host-injected skins
// are reconstructed from the very segments that advertise them: a --play
// dump must not tell the model to call actor_spawn while showing an empty
// tool list.
func TestToolManifestSegments_HostSkins(t *testing.T) {
	segs := toolManifestSegments(core.Registry{}, []PromptSegment{{Source: "cast", Text: "You have a CAST…"}})
	if len(segs) != 2 || segs[0].Source != "tools:host" || !strings.Contains(segs[0].Text, "actor_spawn") {
		t.Fatalf("cast addendum should surface actor_spawn as a host tool: %+v", segs)
	}
	if segs[1].Source != "tools:note" {
		t.Errorf("runtime-merge note missing: %+v", segs)
	}

	segs = toolManifestSegments(core.Registry{}, []PromptSegment{{Source: "auto-swarm", Text: "…"}})
	if len(segs) != 2 || !strings.Contains(segs[0].Text, "swarm_spawn") {
		t.Fatalf("auto-swarm addendum should surface swarm_spawn: %+v", segs)
	}
}

func hasSource(segs []PromptSegment, src string) bool {
	for _, s := range segs {
		if s.Source == src {
			return true
		}
	}
	return false
}

// TestBuildPromptManifest asserts the full assembly offline — the whole point
// of the dump: no model, no credential, just the constructed prompt.
func TestBuildPromptManifest(t *testing.T) {
	r := &Resolved{
		SystemSegments: []PromptSegment{
			{Source: "card:system_prompt", Text: "You are Mara."},
			{Source: "charter", Text: "An archivist."},
			{Source: "lore:constant", Text: "<lore>House style.</lore>"},
		},
		loreTriggered: []lore.Entry{
			{Name: "Vault", Keys: []string{"vault"}, Content: "The vault is sealed.", Order: 10, Source: "vault.md"},
		},
		postHistory:  "Stay terse.",
		ToolRegistry: core.Registry{},
	}
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Hello."}}, Meta: map[string]string{"source": "card:greeting"}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "tell me about the vault"}}},
	}
	m := r.BuildPromptManifest(msgs)

	sys := m.section("system")
	if sys == nil || !hasSource(sys.Segments, "card:system_prompt") || !hasSource(sys.Segments, "lore:constant") {
		t.Errorf("system section missing expected sources: %+v", sys)
	}
	mm := m.section("messages")
	if mm == nil || len(mm.Segments) != 2 || mm.Segments[0].Source != "card:greeting" || mm.Segments[1].Role != "user" {
		t.Errorf("messages section wrong: %+v", mm)
	}
	tail := m.section("tail")
	if tail == nil || len(tail.Segments) != 2 {
		t.Fatalf("tail should be triggered lore + PHI: %+v", tail)
	}
	if !strings.Contains(tail.Segments[0].Source, "vault.md") || !strings.Contains(tail.Segments[0].Text, "vault is sealed") {
		t.Errorf("triggered lore not fired/labeled: %+v", tail.Segments[0])
	}
	if tail.Segments[1].Source != "card:post_history" {
		t.Errorf("PHI missing from tail: %+v", tail.Segments[1])
	}
}

func TestBuildPromptManifest_NoTriggerWhenKeywordAbsent(t *testing.T) {
	r := &Resolved{
		loreTriggered: []lore.Entry{{Name: "Vault", Keys: []string{"vault"}, Content: "sealed", Order: 10, Source: "vault.md"}},
		ToolRegistry:  core.Registry{},
	}
	msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello there"}}}}
	m := r.BuildPromptManifest(msgs)
	if tail := m.section("tail"); tail != nil && len(tail.Segments) != 0 {
		t.Errorf("no keyword -> empty tail, got %+v", tail.Segments)
	}
}

// The dump prints the portability class so a human can SEE what would reach a
// foreign worker. That is only worth printing if it cannot disagree with what
// the composer would actually do — so it is derived at render time from the
// same PortabilityOf the composer consults, never stored on the segment and
// never recomputed by a second rule.
func TestManifestPortabilityMatchesTheClassifier(t *testing.T) {
	m := PromptManifest{Sections: []PromptSection{
		{Name: "system", Segments: []PromptSegment{
			{Source: SourceIdentityIntro, Text: "You are X."},
			{Source: SourceVessel, Text: "terva carries you."},
			{Source: SourceConventions, Text: "markdown."},
			{Source: SourceAgentsMD, Text: "project rules"},
		}},
		{Name: "tail", Segments: []PromptSegment{
			{Source: "lore:triggered [vault.md]", Text: "sealed"},
		}},
	}}

	txt := m.Text()
	for _, want := range []string{
		"[identity-intro · portable]",            // the mind travels
		"[vessel · harness-local]",               // the ship does not
		"[conventions · harness-local]",          // names terva's tools and terva's screen
		"[agents-md · discovery-owned]",          // point at the path, don't paste the file
		"lore:triggered [vault.md] · no-analog]", // nowhere to put it abroad
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("dump should label %q:\n%s", want, txt)
		}
	}

	// And the JSON view agrees with the classifier segment for segment.
	var back struct {
		Sections []struct {
			Name     string `json:"name"`
			Segments []struct {
				Source      string `json:"source"`
				Portability string `json:"portability"`
			} `json:"segments"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(m.JSON()), &back); err != nil {
		t.Fatalf("json invalid: %v", err)
	}
	for _, sec := range back.Sections {
		for _, seg := range sec.Segments {
			if want := string(PortabilityOf(seg.Source)); seg.Portability != want {
				t.Errorf("%s/%s dumped as %q but the classifier says %q — the dump must not be able to disagree with the composer",
					sec.Name, seg.Source, seg.Portability, want)
			}
		}
	}
}

// The conversation is not a segment terva assembled from a labeled source, so
// the table is not about it. Classifying it anyway would print "harness-local"
// (the fail-closed default) beside every user turn — an answer to a question
// nobody asked, that reads like a finding.
func TestManifestDoesNotClassifyMessagesOrTools(t *testing.T) {
	m := PromptManifest{Sections: []PromptSection{
		{Name: "messages", Segments: []PromptSegment{{Source: "user", Role: "user", Text: "fix the bug"}}},
		{Name: "tools", Segments: []PromptSegment{{Source: "tools", Text: "bash, edit, read"}}},
	}}
	if txt := m.Text(); strings.Contains(txt, "harness-local") {
		t.Errorf("messages/tools must not carry a portability class:\n%s", txt)
	}
	if js := m.JSON(); strings.Contains(js, "portability") {
		t.Errorf("messages/tools must not carry a portability class in json:\n%s", js)
	}
}
