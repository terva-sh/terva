package card

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"reflect"
	"testing"
)

const (
	syntheticJSONPath = "../../../examples/cards/aava-v2.json"
	syntheticPNGPath  = "../../../examples/cards/aava-v2.png"
)

// embedCharaPNG returns a small VALID PNG (a real 64x64 image) with the card
// JSON embedded in a `chara` tEXt chunk (base64) — the SillyTavern convention.
// Used to (re)generate the shipped PNG fixture in lockstep with the JSON.
func embedCharaPNG(cardJSON []byte) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 0x11, G: 0x2a, B: 0x3a, A: 0xff}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	base := buf.Bytes()

	text := []byte("chara\x00" + base64.StdEncoding.EncodeToString(cardJSON))
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(text)))
	chunk.WriteString("tEXt")
	chunk.Write(text)
	crc := crc32.NewIEEE()
	crc.Write([]byte("tEXt"))
	crc.Write(text)
	_ = binary.Write(&chunk, binary.BigEndian, crc.Sum32())

	// Insert the tEXt chunk immediately before the IEND chunk (its 4-byte
	// length field sits 4 bytes ahead of the "IEND" type marker).
	iend := bytes.LastIndex(base, []byte("IEND"))
	if iend < 4 {
		return nil, errors.New("png: no IEND chunk")
	}
	insertAt := iend - 4
	out := make([]byte, 0, len(base)+chunk.Len())
	out = append(out, base[:insertAt]...)
	out = append(out, chunk.Bytes()...)
	out = append(out, base[insertAt:]...)
	return out, nil
}

// TestSyntheticCard covers the shipped best-practices fixture: it must exercise
// the full CCv2 surface, and the PNG and JSON versions must parse identically
// (the regression the PNG version exists for). Regenerate the PNG with:
//
//	UPDATE_FIXTURES=1 go test ./packages/agent/card/
func TestSyntheticCard(t *testing.T) {
	fromJSON, err := Load(syntheticJSONPath)
	if err != nil {
		t.Fatalf("load JSON fixture: %v", err)
	}

	if fromJSON.Name != "Aava" || fromJSON.SpecVersion != "2.0" {
		t.Errorf("name/spec = %q / %q", fromJSON.Name, fromJSON.SpecVersion)
	}
	if fromJSON.Description == "" || fromJSON.Personality == "" || fromJSON.Scenario == "" || fromJSON.FirstMes == "" || fromJSON.MesExample == "" {
		t.Error("a descriptive field is empty")
	}
	if fromJSON.SystemPrompt == "" || fromJSON.PostHistoryInstructions == "" {
		t.Error("system_prompt / post_history_instructions missing")
	}
	if len(fromJSON.AlternateGreetings) != 2 {
		t.Errorf("alternate_greetings = %d, want 2", len(fromJSON.AlternateGreetings))
	}
	if fromJSON.CreatorNotes == "" || len(fromJSON.Extensions) == 0 {
		t.Error("creator_notes / extensions missing")
	}
	if fromJSON.CharacterBook == nil || len(fromJSON.CharacterBook.Entries) != 3 {
		t.Fatalf("character_book should have 3 entries")
	}

	// The book must represent all three activation shapes.
	var keyed, selective, constant int
	for _, e := range fromJSON.CharacterBook.Entries {
		switch {
		case e.Constant:
			constant++
		case e.Selective:
			selective++
			if len(e.SecondaryKeys) == 0 {
				t.Errorf("selective entry %q has no secondary_keys", e.Name)
			}
		default:
			keyed++
		}
	}
	if keyed != 1 || selective != 1 || constant != 1 {
		t.Errorf("book entry mix = keyed %d / selective %d / constant %d (want 1/1/1)", keyed, selective, constant)
	}

	if os.Getenv("UPDATE_FIXTURES") != "" {
		raw, err := os.ReadFile(syntheticJSONPath)
		if err != nil {
			t.Fatal(err)
		}
		pngBytes, err := embedCharaPNG(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(syntheticPNGPath, pngBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s (%d bytes)", syntheticPNGPath, len(pngBytes))
	}

	fromPNG, err := Load(syntheticPNGPath)
	if err != nil {
		t.Fatalf("load PNG fixture (run `UPDATE_FIXTURES=1 go test ./packages/agent/card/` to regenerate): %v", err)
	}
	if !reflect.DeepEqual(fromJSON, fromPNG) {
		t.Error("JSON and PNG fixtures parse to different Cards")
	}
}
