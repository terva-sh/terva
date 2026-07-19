package card

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
)

// TestMarshalRoundTripsExtensions is the round-trip rule the library store leans
// on: Marshal(ParseJSON(x)) re-parses to the same card, and unknown `extensions`
// survive verbatim (a vendor key terva doesn't understand must not be dropped on
// an edit).
func TestMarshalRoundTripsExtensions(t *testing.T) {
	src := `{"spec":"chara_card_v2","spec_version":"2.0","data":{
		"name":"Seraphina","description":"a guardian","first_mes":"Hello.",
		"alternate_greetings":["Hi","Hey"],
		"post_history_instructions":"stay in character",
		"character_book":{"name":"lore","entries":[{"keys":["forest"],"content":"deep woods"}]},
		"tags":["fantasy"],"creator":"me",
		"extensions":{"depth_prompt":{"depth":4,"prompt":"secret"},"vendor_key":42}
	}}`
	c, err := ParseJSON([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := ParseJSON(out)
	if err != nil {
		t.Fatalf("marshal output does not re-parse: %v\n%s", err, out)
	}
	if c2.Name != "Seraphina" || c2.FirstMes != "Hello." || c2.PostHistoryInstructions != "stay in character" {
		t.Errorf("core fields lost: %+v", c2)
	}
	if len(c2.AlternateGreetings) != 2 || c2.CharacterBook == nil || len(c2.CharacterBook.Entries) != 1 {
		t.Errorf("structured fields lost: %+v", c2)
	}
	var e1, e2 map[string]any
	if err := json.Unmarshal(c.Extensions, &e1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(c2.Extensions, &e2); err != nil {
		t.Fatalf("extensions dropped on round-trip: %v (%s)", err, c2.Extensions)
	}
	if !reflect.DeepEqual(e1, e2) {
		t.Errorf("extensions changed across round-trip:\n%v\n%v", e1, e2)
	}
}

// TestMarshalDeterministic — the library id is a content hash of Marshal's
// output, so the same card must always marshal to the same bytes.
func TestMarshalDeterministic(t *testing.T) {
	c, err := ParseJSON([]byte(v2Mara))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Marshal(c)
	b, _ := Marshal(c)
	if string(a) != string(b) {
		t.Error("Marshal must be deterministic (it is content-hashed for the library id)")
	}
}

// TestIsPNGAndLoadBytes covers the byte-level entry points the store uses to
// import an upload without touching the filesystem.
func TestIsPNGAndLoadBytes(t *testing.T) {
	if IsPNG([]byte(v2Mara)) {
		t.Error("a JSON card must not sniff as PNG")
	}
	if c, err := LoadBytes([]byte(v2Mara)); err != nil || c.Name != "Mara" {
		t.Fatalf("LoadBytes(JSON) = %+v, %v", c, err)
	}

	png := makePNG(
		pngChunk("tEXt", []byte("chara\x00"+base64.StdEncoding.EncodeToString([]byte(v2Mara)))),
		pngChunk("IEND", nil),
	)
	if !IsPNG(png) {
		t.Error("a built chara PNG must sniff as PNG")
	}
	if c, err := LoadBytes(png); err != nil || c.Name != "Mara" {
		t.Fatalf("LoadBytes(PNG) = %+v, %v", c, err)
	}
}
