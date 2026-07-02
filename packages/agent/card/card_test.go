package card

import "testing"

func TestParseJSON_V2(t *testing.T) {
	data := `{"spec":"chara_card_v2","spec_version":"2.0","data":{
		"name":"Mara","description":"archivist","first_mes":"hi",
		"creator_notes":"do not ship",
		"alternate_greetings":["hey","yo"],
		"extensions":{"terva.sh/harness":{"authority":"style_hint"}},
		"character_book":{"name":"lore","entries":[
			{"keys":["vault"],"content":"the vault is sealed","insertion_order":10,"constant":false}
		]}}}`
	c, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Mara" || c.SpecVersion != "2.0" {
		t.Errorf("name/spec = %q / %q", c.Name, c.SpecVersion)
	}
	if len(c.AlternateGreetings) != 2 {
		t.Errorf("alt greetings = %v", c.AlternateGreetings)
	}
	if c.CreatorNotes != "do not ship" {
		t.Errorf("creator_notes = %q", c.CreatorNotes)
	}
	if c.CharacterBook == nil || len(c.CharacterBook.Entries) != 1 || c.CharacterBook.Entries[0].Keys[0] != "vault" {
		t.Fatalf("character_book not parsed: %+v", c.CharacterBook)
	}
	if len(c.Extensions) == 0 {
		t.Errorf("extensions should be retained verbatim (never interpreted)")
	}
}

func TestParseJSON_V1(t *testing.T) {
	data := `{"name":"Old","description":"d","personality":"p","scenario":"s","first_mes":"hello","mes_example":"<START>"}`
	c, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Old" || c.SpecVersion != "" || c.FirstMes != "hello" {
		t.Errorf("V1 upgrade wrong: %+v", c)
	}
}

func TestParseJSON_Errors(t *testing.T) {
	if _, err := ParseJSON([]byte(`{"spec":"chara_card_v2","data":{"description":"x"}}`)); err == nil {
		t.Error("expected missing-name error")
	}
	if _, err := ParseJSON([]byte(`not json`)); err == nil {
		t.Error("expected invalid-JSON error")
	}
}

func TestSubstitute(t *testing.T) {
	got := Substitute("{{char}} greets {{USER}}, <BOT> waves at <user>", "Mara", "Drew")
	want := "Mara greets Drew, Mara waves at Drew"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if Substitute("", "a", "b") != "" {
		t.Errorf("empty in => empty out")
	}
}

func TestApplyOriginal(t *testing.T) {
	got := ApplyOriginal("before {{original}} middle {{ORIGINAL}} after", "DEFAULT")
	want := "before DEFAULT middle  after"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestExampleBlocks(t *testing.T) {
	in := "<START>\n{{user}}: hi\n{{char}}: hello\n<start>\n{{user}}: bye\n{{char}}: see ya"
	blocks := ExampleBlocks(in)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (case-insensitive <START>), got %d: %v", len(blocks), blocks)
	}
	if blocks[0] != "{{user}}: hi\n{{char}}: hello" {
		t.Errorf("block0 = %q (delimiter not stripped/trimmed)", blocks[0])
	}
	if ExampleBlocks("") != nil || ExampleBlocks("   ") != nil {
		t.Error("empty/blank input should yield nil")
	}
}
