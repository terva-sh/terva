package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/testsupport"
)

func TestLoadCardIdentity(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "mara.json")
	body := `{"spec":"chara_card_v2","spec_version":"2.0","data":{
		"name":"Mara","description":"{{char}} is a meticulous archivist.",
		"personality":"observant, patient","scenario":"{{char}} and {{user}} explore a vault",
		"first_mes":"*{{char}} lifts a folder.* Shall we read it, {{user}}?",
		"system_prompt":"You are {{char}}, keeper of secrets. {{original}}",
		"post_history_instructions":"Stay terse, {{char}}.",
		"mes_example":"<START>\n{{user}}: what do you see?\n{{char}}: The paper is wrong.",
		"character_book":{"entries":[
			{"keys":["vault","{{char}}"],"content":"The vault is sealed.","insertion_order":10},
			{"constant":true,"content":"Always speak in a hush.","insertion_order":5},
			{"keys":["ghost"],"content":"skip me","enabled":false}
		]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	ci, err := loadCardIdentity(path, ExperienceChat, 0, "User")
	if err != nil {
		t.Fatal(err)
	}
	if ci.Persona.Name != "Mara" {
		t.Errorf("name = %q", ci.Persona.Name)
	}
	if strings.Contains(ci.Persona.Charter, "{{char}}") || strings.Contains(ci.Persona.Charter, "{{user}}") {
		t.Errorf("macros not substituted in charter: %q", ci.Persona.Charter)
	}
	if !strings.Contains(ci.Persona.Charter, "Mara is a meticulous archivist") {
		t.Errorf("{{char}} not resolved / description missing: %q", ci.Persona.Charter)
	}
	if !strings.Contains(ci.Persona.Charter, "Personality:") || !strings.Contains(ci.Persona.Charter, "Scenario:") {
		t.Errorf("charter sections missing: %q", ci.Persona.Charter)
	}
	if !strings.Contains(ci.Persona.Charter, "Example dialogue:") || !strings.Contains(ci.Persona.Charter, "The paper is wrong") {
		t.Errorf("example dialogue missing from charter: %q", ci.Persona.Charter)
	}
	if strings.Contains(ci.Persona.Charter, "<START>") {
		t.Errorf("<START> delimiter not stripped from charter: %q", ci.Persona.Charter)
	}
	if !strings.Contains(ci.greeting, "Mara lifts a folder") || !strings.Contains(ci.greeting, "User") || strings.Contains(ci.greeting, "{{user}}") {
		t.Errorf("greeting substitution wrong (want {{char}}->Mara, {{user}}->User): %q", ci.greeting)
	}
	// system_prompt -> intro override: {{char}} substituted, {{original}}
	// expanded to the short brand-free framing (NOT terva's branded intro — a
	// card must never be told it's "a mind terva carries").
	if !strings.Contains(ci.introOverride, "Mara, keeper of secrets") {
		t.Errorf("introOverride missing/unsubstituted: %q", ci.introOverride)
	}
	if strings.Contains(ci.introOverride, "{{original}}") || strings.Contains(ci.introOverride, "terva") {
		t.Errorf("{{original}} should expand to brand-free framing, not terva branding: %q", ci.introOverride)
	}
	if !strings.Contains(ci.introOverride, "speak and act naturally and in character") {
		t.Errorf("{{original}} not expanded to the immersive framing: %q", ci.introOverride)
	}
	if ci.introSource != "card:system_prompt" {
		t.Errorf("introSource = %q, want card:system_prompt", ci.introSource)
	}
	if ci.postHistory != "Stay terse, Mara." {
		t.Errorf("postHistory = %q, want %q", ci.postHistory, "Stay terse, Mara.")
	}
	// character_book: two enabled entries (one keyed, one constant); the
	// disabled entry is dropped. Keys had {{char}} substituted.
	if len(ci.lore) != 2 {
		t.Fatalf("expected 2 lore entries, got %d: %+v", len(ci.lore), ci.lore)
	}
	var constants, keyed int
	for _, e := range ci.lore {
		if e.Constant {
			constants++
		} else {
			keyed++
			if e.Source != "card" {
				t.Errorf("lore source = %q, want card", e.Source)
			}
			hasMara := false
			for _, k := range e.Keys {
				if k == "Mara" {
					hasMara = true
				}
			}
			if !hasMara {
				t.Errorf("{{char}} key not substituted to Mara: %v", e.Keys)
			}
		}
	}
	if constants != 1 || keyed != 1 {
		t.Errorf("expected 1 constant + 1 keyed, got %d/%d", constants, keyed)
	}
}

func TestCardBookToLore_SelectiveGatesSecondaryKeys(t *testing.T) {
	// Per CCv2, secondary_keys apply only when `selective` is true. ST
	// serializes leftover keysecondary even when selective is toggled off, so
	// a selective:false entry must fire on its primary Key alone — importing
	// its secondary_keys would silently suppress it (the engine treats any
	// non-empty SecondaryKeys as a mandatory gate).
	path := filepath.Join(testsupport.TempDir(t), "s.json")
	body := `{"spec":"chara_card_v2","spec_version":"2.0","data":{
		"name":"Ada","first_mes":"Hello.",
		"character_book":{"entries":[
			{"keys":["dragon"],"secondary_keys":["fire"],"selective":false,"content":"leftover secondaries","insertion_order":1},
			{"keys":["dragon"],"secondary_keys":["fire"],"selective":true,"content":"gated","insertion_order":2}
		]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ci, err := loadCardIdentity(path, ExperienceChat, 0, "User")
	if err != nil {
		t.Fatal(err)
	}
	if len(ci.lore) != 2 {
		t.Fatalf("expected 2 lore entries, got %d: %+v", len(ci.lore), ci.lore)
	}
	for _, e := range ci.lore {
		switch e.Content {
		case "leftover secondaries":
			if len(e.SecondaryKeys) != 0 {
				t.Errorf("selective:false entry must drop secondary_keys, got %v", e.SecondaryKeys)
			}
		case "gated":
			if len(e.SecondaryKeys) != 1 || e.SecondaryKeys[0] != "fire" {
				t.Errorf("selective:true entry must keep secondary_keys, got %v", e.SecondaryKeys)
			}
		default:
			t.Errorf("unexpected entry %q", e.Content)
		}
	}
}

func TestLoadCardIdentity_BadPath(t *testing.T) {
	if _, err := loadCardIdentity(filepath.Join(testsupport.TempDir(t), "nope.json"), ExperienceChat, 0, "User"); err == nil {
		t.Error("expected error for a missing card file")
	}
}

func TestLoadCardIdentity_GreetingSelection(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "g.json")
	body := `{"spec":"chara_card_v2","spec_version":"2.0","data":{
		"name":"Ada","first_mes":"Hello there.","alternate_greetings":["Hey!","Yo."]}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for idx, want := range map[int]string{0: "Hello there.", 1: "Hey!", 2: "Yo."} {
		ci, err := loadCardIdentity(path, ExperienceChat, idx, "User")
		if err != nil {
			t.Fatalf("idx %d: %v", idx, err)
		}
		if ci.greeting != want {
			t.Errorf("greeting[%d] = %q, want %q", idx, ci.greeting, want)
		}
	}
	if _, err := loadCardIdentity(path, ExperienceChat, 3, "User"); err == nil {
		t.Error("--greeting 3 should be out of range (only 0..2)")
	}
}

func TestLoadCardIdentity_NoSystemPrompt(t *testing.T) {
	// A card that supplies no system_prompt must still never inherit terva's
	// branded intro: its intro slot is the short brand-free framing.
	path := filepath.Join(testsupport.TempDir(t), "n.json")
	body := `{"spec":"chara_card_v2","spec_version":"2.0","data":{
		"name":"Ada","first_mes":"Hello.","description":"a quiet archivist"}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ci, err := loadCardIdentity(path, ExperienceChat, 0, "User")
	if err != nil {
		t.Fatal(err)
	}
	if ci.introSource != "card:framing" {
		t.Errorf("introSource = %q, want card:framing", ci.introSource)
	}
	if ci.introOverride == "" || strings.Contains(ci.introOverride, "terva") {
		t.Errorf("no-system_prompt intro should be brand-free framing, got %q", ci.introOverride)
	}
}

// The card's book-level lore config is primary (proposal open question #3:
// the card's absolute token_budget is the card's own number); a lore.json
// from another tier fills only the fields the card leaves unset — it must
// not silently shadow the card's budget for the card's own entries.
func TestMergeCardLoreConfig(t *testing.T) {
	fileCfg := lore.Config{ScanDepth: 4, TokenBudget: 900}

	// Card sets only its budget: the card's budget wins; the file's
	// scan_depth fills the field the card left unset.
	got := mergeCardLoreConfig(fileCfg, lore.Config{TokenBudget: 300})
	if got.TokenBudget != 300 || got.ScanDepth != 4 {
		t.Errorf("card budget must win, file fills the rest: %+v", got)
	}
	// Card silent: file config passes through untouched.
	if got := mergeCardLoreConfig(fileCfg, lore.Config{}); got != fileCfg {
		t.Errorf("silent card must not disturb the file config: %+v", got)
	}
	// No file config: the card's own config stands alone.
	card := lore.Config{ScanDepth: 2, TokenBudget: 300, RecursiveScanning: true}
	if got := mergeCardLoreConfig(lore.Config{}, card); got != card {
		t.Errorf("card config should stand alone: %+v", got)
	}
	// RecursiveScanning ORs: the card can enable it, not veto it.
	if got := mergeCardLoreConfig(lore.Config{RecursiveScanning: true}, lore.Config{}); !got.RecursiveScanning {
		t.Error("a silent card must not veto a tier's recursive_scanning")
	}
}

func TestResolveCardUserName(t *testing.T) {
	// --as wins over the persisted config.
	if got := resolveCardUserName(Args{As: "Kael"}, config.Config{UserName: "Saved"}); got != "Kael" {
		t.Errorf("flag should win: got %q", got)
	}
	// config is used when there's no flag.
	if got := resolveCardUserName(Args{}, config.Config{UserName: "Saved"}); got != "Saved" {
		t.Errorf("config should be used: got %q", got)
	}
	// nothing set -> the literal "User".
	if got := resolveCardUserName(Args{}, config.Config{}); got != "User" {
		t.Errorf("fallback should be User: got %q", got)
	}
	// blank/whitespace flag and config both fall through to "User".
	if got := resolveCardUserName(Args{As: "  "}, config.Config{UserName: "  "}); got != "User" {
		t.Errorf("blank flag/config should fall back to User: got %q", got)
	}
}

func TestPerTurnContext_PHIAndEmpty(t *testing.T) {
	// PHI-only: the provider emits the PHI without touching the agent (no
	// triggered lore, so ag.Messages() is never called).
	r := &Resolved{postHistory: "Stay in character."}
	fn := r.PerTurnContext(nil)
	if fn == nil {
		t.Fatal("expected a provider when PHI is set")
	}
	if got := fn(); got != "Stay in character." {
		t.Errorf("PHI provider = %q", got)
	}
	// Neither lore nor PHI -> nil provider (composeEphemeral skips it).
	if (&Resolved{}).PerTurnContext(nil) != nil {
		t.Error("empty run should yield a nil per-turn provider")
	}
}
