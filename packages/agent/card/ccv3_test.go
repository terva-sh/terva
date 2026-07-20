package card

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const v3Doc = `{"spec":"chara_card_v3","spec_version":"3.0","data":{
	"name":"Ivy","description":"a botanist","first_mes":"Hello.",
	"nickname":"Iv","source":["https://example.test/ivy"],
	"group_only_greetings":["Hi all."],
	"creator_notes_multilingual":{"ja":"こんにちは"},
	"creation_date":1700000000,
	"assets":[{"type":"icon","uri":"ccdefault:","name":"main","ext":"png"},
	          {"type":"emotion","uri":"embeded://happy.png","name":"happy","ext":"png"}],
	"character_book":{"name":"lore","entries":[
	  {"keys":["forest"],"content":"@@depth 4\ndeep woods","use_regex":true}]}}}`

func ccv3Chunk(doc string) []byte {
	return pngChunk("tEXt", []byte("ccv3\x00"+base64.StdEncoding.EncodeToString([]byte(doc))))
}

func charaChunk(doc string) []byte {
	return pngChunk("tEXt", []byte("chara\x00"+base64.StdEncoding.EncodeToString([]byte(doc))))
}

// Severity 1: a PNG carrying ONLY a ccv3 chunk used to fail outright with
// "no character metadata", because ReadPNG matched the `chara` keyword alone.
func TestReadPNG_CCv3Only(t *testing.T) {
	png := makePNG(ccv3Chunk(v3Doc), pngChunk("IEND", nil))
	got, err := ReadPNG(png)
	if err != nil {
		t.Fatalf("a ccv3-only PNG must import: %v", err)
	}
	c, err := ParseJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Ivy" || c.Nickname != "Iv" {
		t.Errorf("V3 fields lost: name=%q nickname=%q", c.Name, c.Nickname)
	}
}

// Severity 2: the layout V3 writers actually emit — ccv3 beside a V2 chara
// back-compat copy. The spec says a reader that finds both SHOULD use ccv3;
// terva used to take the chara one and silently import the downgrade.
func TestReadPNG_PrefersCCv3OverChara(t *testing.T) {
	v2Twin := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Ivy","description":"DOWNGRADED"}}`
	// chara first in the file, so a first-match-wins reader would take the wrong one.
	png := makePNG(charaChunk(v2Twin), ccv3Chunk(v3Doc), pngChunk("IEND", nil))
	got, err := ReadPNG(png)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	if c.Description == "DOWNGRADED" {
		t.Error("took the chara back-compat chunk; ccv3 must win")
	}
	if c.SpecVersion != "3.0" {
		t.Errorf("spec_version = %q, want 3.0", c.SpecVersion)
	}
	// The back-compat chara chunk is NOT an ambiguity to report — distinct
	// keywords with a defined precedence, unlike two chara chunks.
	if n := CountCharaChunks(png); n != 1 {
		t.Errorf("ccv3+chara should not read as an ambiguous duplicate, got count %d", n)
	}
}

// Severity 3: V3 fields survive the parse AND the re-marshal. Marshal used to
// hardcode chara_card_v2, so the import wrote a V2 card.json and made the loss
// permanent.
func TestV3RoundTrip(t *testing.T) {
	c, err := ParseJSON([]byte(v3Doc))
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsV3() {
		t.Fatal("card should report as V3")
	}
	out, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec        string `json:"spec"`
		SpecVersion string `json:"spec_version"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Spec != "chara_card_v3" || doc.SpecVersion != "3.0" {
		t.Errorf("a V3 card must stay V3 on marshal, got %s/%s", doc.Spec, doc.SpecVersion)
	}
	back, err := ParseJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Nickname != "Iv" {
		t.Errorf("nickname lost: %q", back.Nickname)
	}
	if len(back.Source) != 1 || back.Source[0] != "https://example.test/ivy" {
		t.Errorf("source lost: %v", back.Source)
	}
	if len(back.GroupOnlyGreetings) != 1 {
		t.Errorf("group_only_greetings lost: %v", back.GroupOnlyGreetings)
	}
	if back.CreatorNotesMultilingual["ja"] != "こんにちは" {
		t.Errorf("creator_notes_multilingual lost: %v", back.CreatorNotesMultilingual)
	}
	if back.CreationDate == nil || *back.CreationDate != 1700000000 {
		t.Errorf("creation_date lost: %v", back.CreationDate)
	}
	if len(back.Assets) != 2 {
		t.Errorf("assets lost: %v", back.Assets)
	}
	if len(back.CharacterBook.Entries) != 1 || !back.CharacterBook.Entries[0].UseRegex {
		t.Errorf("lorebook use_regex lost: %+v", back.CharacterBook)
	}
}

// A V2 card must not grow V3 keys just because the struct now has the fields.
func TestV2RoundTripStaysV2(t *testing.T) {
	c, err := ParseJSON([]byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Mara"}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "chara_card_v2") {
		t.Error("a V2 card must stay V2")
	}
	for _, k := range []string{"nickname", "group_only_greetings", "assets", "source", "creation_date", "use_regex"} {
		if strings.Contains(s, `"`+k+`"`) {
			t.Errorf("V2 card grew a V3 key %q:\n%s", k, s)
		}
	}
}

// Exporting a V3 card writes ccv3 PLUS a V2 chara downgrade, so the file stays
// openable in V2-only tools without the V3 reader losing anything.
func TestWritePNG_V3WritesBothChunks(t *testing.T) {
	c, err := ParseJSON([]byte(v3Doc))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	src := makePNG(charaChunk(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"stale"}}`), pngChunk("IEND", nil))
	out, err := WritePNG(src, raw)
	if err != nil {
		t.Fatal(err)
	}
	found, err := scanCardChunks(out)
	if err != nil {
		t.Fatal(err)
	}
	if found.ccv3Count != 1 || found.charaCount != 1 {
		t.Fatalf("want one ccv3 + one chara, got ccv3=%d chara=%d", found.ccv3Count, found.charaCount)
	}
	// Reading it back gets the V3 record, not the downgrade.
	back, err := ReadPNG(out)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := ParseJSON(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bc.IsV3() || bc.Nickname != "Iv" {
		t.Errorf("round-trip lost V3: v3=%v nickname=%q", bc.IsV3(), bc.Nickname)
	}
	// And the chara chunk is a genuine V2 doc with the V3-only keys stripped,
	// not the V3 document wearing a V2 label.
	twin, err := decodeChara(found.chara)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(twin), "chara_card_v3") || strings.Contains(string(twin), "nickname") {
		t.Errorf("the back-compat chunk must be a real V2 downgrade:\n%s", twin)
	}
	tc, err := ParseJSON(twin)
	if err != nil || tc.Name != "Ivy" {
		t.Errorf("back-compat chunk must still be a usable card: %v / %+v", err, tc)
	}
}

// The unsupported-feature report names what terva carries but does not act on.
func TestUnsupportedV3Features(t *testing.T) {
	c, err := ParseJSON([]byte(v3Doc))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(UnsupportedV3Features(c), "\n")
	for _, want := range []string{"regex", "@@decorators", "asset", "group-chat"} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	// The spec's default asset (a ccdefault: main icon) is exactly what terva
	// already serves, so a card declaring only that earns no asset warning.
	plain, err := ParseJSON([]byte(`{"spec":"chara_card_v3","spec_version":"3.0","data":{"name":"Ivy",
		"assets":[{"type":"icon","uri":"ccdefault:","name":"main","ext":"png"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r := UnsupportedV3Features(plain); len(r) != 0 {
		t.Errorf("the default icon asset should not warn, got %v", r)
	}
	// A V2 card reports nothing.
	v2, _ := ParseJSON([]byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Mara"}}`))
	if r := UnsupportedV3Features(v2); len(r) != 0 {
		t.Errorf("a V2 card should report nothing, got %v", r)
	}
}

const (
	syntheticV3JSONPath = "../../../examples/cards/aava-v3.json"
	syntheticV3PNGPath  = "../../../examples/cards/aava-v3.png"
)

// TestSyntheticCardV3 covers the shipped CCv3 fixture, the V3 sibling of
// TestSyntheticCard. It must exercise the V3-only surface, and the PNG and JSON
// versions must parse identically. Regenerate the PNG with:
//
//	UPDATE_FIXTURES=1 go test ./packages/agent/card/
//
// ⚠️ SYNTHETIC — see examples/cards/README.md. This fixture was authored from the
// CCv3 spec, not exported by a real V3 editor, so it proves terva reads what the
// spec DESCRIBES rather than what the ecosystem actually EMITS. Those diverged
// once already and cost real debugging: a live chub.ai card turned out to carry
// two `chara` chunks holding different revisions, something no fixture predicted.
// Replace or supplement this with a card exported by SillyTavern/RisuAI when one
// is to hand, and keep whatever surprises it brings.
func TestSyntheticCardV3(t *testing.T) {
	fromJSON, err := Load(syntheticV3JSONPath)
	if err != nil {
		t.Fatalf("load V3 JSON fixture: %v", err)
	}

	if fromJSON.Name != "Aava" || !fromJSON.IsV3() {
		t.Errorf("name/spec = %q / %q", fromJSON.Name, fromJSON.SpecVersion)
	}
	// The V3-only surface, so the fixture cannot quietly decay into a V2 card.
	if fromJSON.Nickname == "" {
		t.Error("nickname missing")
	}
	if len(fromJSON.Source) == 0 {
		t.Error("source missing")
	}
	if len(fromJSON.GroupOnlyGreetings) == 0 {
		t.Error("group_only_greetings missing")
	}
	if len(fromJSON.CreatorNotesMultilingual) < 2 {
		t.Errorf("creator_notes_multilingual should carry >1 language, got %v", fromJSON.CreatorNotesMultilingual)
	}
	if fromJSON.CreationDate == nil || fromJSON.ModificationDate == nil {
		t.Error("creation_date / modification_date missing")
	}
	if len(nonDefaultAssets(fromJSON.Assets)) < 2 {
		t.Errorf("fixture should carry assets beyond the default icon, got %v", fromJSON.Assets)
	}
	if fromJSON.CharacterBook == nil || len(fromJSON.CharacterBook.Entries) != 4 {
		t.Fatal("character_book should have 4 entries")
	}

	// The book must represent every V3 activation shape the reader has to cope
	// with: a plain keyed entry, a regex-keyed one, a decorated one, a constant.
	var keyed, regex, decorated, constant int
	for _, e := range fromJSON.CharacterBook.Entries {
		switch {
		case e.Constant:
			constant++
		case e.UseRegex:
			regex++
		case hasDecorator(e.Content):
			decorated++
		default:
			keyed++
		}
	}
	if keyed != 1 || regex != 1 || decorated != 1 || constant != 1 {
		t.Errorf("book entry mix = keyed %d / regex %d / decorated %d / constant %d (want 1/1/1/1)", keyed, regex, decorated, constant)
	}

	// Every unsupported-feature branch is reachable from this one fixture, so the
	// warning text cannot rot unnoticed.
	report := strings.Join(UnsupportedV3Features(fromJSON), "\n")
	for _, want := range []string{"regex", "@@decorators", "asset", "group-chat"} {
		if !strings.Contains(report, want) {
			t.Errorf("fixture no longer exercises the %q warning:\n%s", want, report)
		}
	}

	if os.Getenv("UPDATE_FIXTURES") != "" {
		raw, err := os.ReadFile(syntheticV3JSONPath)
		if err != nil {
			t.Fatal(err)
		}
		base, err := embedCharaPNG(nil) // a real 64x64 image with no card chunk yet
		if err != nil {
			t.Fatal(err)
		}
		// WritePNG lays out a V3 card the way V3 writers do: a `ccv3` chunk plus a
		// V2 downgrade under `chara`. That is the shape the reader must handle, so
		// it is the shape the fixture ships as.
		pngBytes, err := WritePNG(base, raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(syntheticV3PNGPath, pngBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s (%d bytes)", syntheticV3PNGPath, len(pngBytes))
	}

	fromPNG, err := Load(syntheticV3PNGPath)
	if err != nil {
		t.Fatalf("load V3 PNG fixture (run `UPDATE_FIXTURES=1 go test ./packages/agent/card/` to regenerate): %v", err)
	}
	if !reflect.DeepEqual(fromJSON, fromPNG) {
		t.Error("V3 JSON and PNG fixtures parse to different Cards")
	}

	// The shipped PNG really is the dual-chunk layout, and a V2-only reader
	// following the `chara` chunk still finds a usable card.
	raw, err := os.ReadFile(syntheticV3PNGPath)
	if err != nil {
		t.Fatal(err)
	}
	found, err := scanCardChunks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if found.ccv3Count != 1 || found.charaCount != 1 {
		t.Fatalf("fixture should ship ccv3+chara, got ccv3=%d chara=%d", found.ccv3Count, found.charaCount)
	}
	twin, err := decodeChara(found.chara)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := ParseJSON(twin)
	if err != nil || tc.Name != "Aava" || tc.IsV3() {
		t.Errorf("back-compat chunk must be a usable V2 card: %v / v3=%v", err, tc.IsV3())
	}
}
