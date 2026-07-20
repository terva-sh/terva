package card

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func pngChunk(ctype string, data []byte) []byte {
	var b bytes.Buffer
	var lenb [4]byte
	binary.BigEndian.PutUint32(lenb[:], uint32(len(data)))
	b.Write(lenb[:])
	b.WriteString(ctype)
	b.Write(data)
	b.Write([]byte{0, 0, 0, 0}) // CRC placeholder (ReadPNG ignores CRC content)
	return b.Bytes()
}

func makePNG(chunks ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteString(pngSignature)
	for _, c := range chunks {
		b.Write(c)
	}
	return b.Bytes()
}

const v2Mara = `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Mara"}}`

func TestReadPNG_tEXt_base64(t *testing.T) {
	text := "chara\x00" + base64.StdEncoding.EncodeToString([]byte(v2Mara))
	png := makePNG(pngChunk("tEXt", []byte(text)), pngChunk("IEND", nil))
	got, err := ReadPNG(png)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := ParseJSON(got); err != nil || c.Name != "Mara" {
		t.Fatalf("parse: %v / %+v", err, c)
	}
}

func TestReadPNG_RejectsOversizeChara(t *testing.T) {
	// An uncompressed tEXt `chara` payload larger than the cap must be rejected
	// (not copied, decoded, or OOM'd) — the same ceiling the compressed paths get.
	big := make([]byte, maxCharaBytes+16)
	for i := range big {
		big[i] = 'A'
	}
	text := append([]byte("chara\x00"), big...)
	png := makePNG(pngChunk("tEXt", text), pngChunk("IEND", nil))
	if _, err := ReadPNG(png); err == nil {
		t.Fatal("oversize `chara` tEXt payload should be rejected by the size cap")
	}
}

func TestReadPNG_tEXt_rawJSON(t *testing.T) {
	// Some encoders store raw JSON rather than base64.
	png := makePNG(pngChunk("tEXt", []byte("chara\x00"+v2Mara)), pngChunk("IEND", nil))
	got, err := ReadPNG(png)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := ParseJSON(got); c.Name != "Mara" {
		t.Fatalf("raw-JSON chara not read: %+v", c)
	}
}

func TestReadPNG_zTXt(t *testing.T) {
	var payload bytes.Buffer
	payload.WriteString("chara")
	payload.WriteByte(0) // keyword terminator
	payload.WriteByte(0) // compression method
	zw := zlib.NewWriter(&payload)
	_, _ = zw.Write([]byte(base64.StdEncoding.EncodeToString([]byte(v2Mara))))
	_ = zw.Close()
	png := makePNG(pngChunk("zTXt", payload.Bytes()), pngChunk("IEND", nil))
	got, err := ReadPNG(png)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := ParseJSON(got); c.Name != "Mara" {
		t.Fatalf("zTXt not read: %+v", c)
	}
}

func TestReadPNG_iTXt_uncompressed(t *testing.T) {
	var p bytes.Buffer
	p.WriteString("chara")
	p.WriteByte(0) // keyword terminator
	p.WriteByte(0) // compression flag = uncompressed
	p.WriteByte(0) // compression method
	p.WriteByte(0) // empty language tag
	p.WriteByte(0) // empty translated keyword
	p.WriteString(base64.StdEncoding.EncodeToString([]byte(v2Mara)))
	png := makePNG(pngChunk("iTXt", p.Bytes()), pngChunk("IEND", nil))
	got, err := ReadPNG(png)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := ParseJSON(got); c.Name != "Mara" {
		t.Fatalf("iTXt not read: %+v", c)
	}
}

func TestReadPNG_Errors(t *testing.T) {
	if _, err := ReadPNG([]byte("not a png")); err == nil {
		t.Error("expected not-a-PNG error")
	}
	noChara := makePNG(pngChunk("tEXt", []byte("Comment\x00hello")), pngChunk("IEND", nil))
	if _, err := ReadPNG(noChara); err == nil {
		t.Error("expected error when no chara chunk present")
	}
}

// A PNG may legally repeat a tEXt keyword, and real cards do: one off chub.ai
// carried two `chara` chunks holding DIFFERENT REVISIONS of the character (one
// system prompt twice the other's, differing first_mes and tags). ReadPNG takes
// the first; CountCharaChunks is how an importer learns a choice was made, so it
// can say so.
func TestCountCharaChunks(t *testing.T) {
	chara := func(name string) []byte {
		doc := `{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"` + name + `"}}`
		return pngChunk("tEXt", []byte("chara\x00"+base64.StdEncoding.EncodeToString([]byte(doc))))
	}

	t.Run("none", func(t *testing.T) {
		if n := CountCharaChunks(makePNG(pngChunk("IEND", nil))); n != 0 {
			t.Errorf("got %d, want 0", n)
		}
	})

	t.Run("one", func(t *testing.T) {
		if n := CountCharaChunks(makePNG(chara("Mara"), pngChunk("IEND", nil))); n != 1 {
			t.Errorf("got %d, want 1", n)
		}
	})

	// The shape of the real card: two chara chunks with other chunks interleaved.
	t.Run("two with an interleaved chunk", func(t *testing.T) {
		png := makePNG(chara("Mara"), pngChunk("iCCP", []byte("profile\x00\x00x")), chara("Other"), pngChunk("IEND", nil))
		if n := CountCharaChunks(png); n != 2 {
			t.Fatalf("got %d, want 2", n)
		}
		// And the FIRST is still what parses out — the count reports the
		// ambiguity, it must not change which record wins.
		got, err := ReadPNG(png)
		if err != nil {
			t.Fatalf("ReadPNG: %v", err)
		}
		if c, err := ParseJSON(got); err != nil || c.Name != "Mara" {
			t.Fatalf("first chunk must win: %v / %+v", err, c)
		}
	})

	// Non-chara text chunks must not inflate the count.
	t.Run("ignores other keywords", func(t *testing.T) {
		png := makePNG(pngChunk("tEXt", []byte("Comment\x00hi")), chara("Mara"), pngChunk("IEND", nil))
		if n := CountCharaChunks(png); n != 1 {
			t.Errorf("got %d, want 1", n)
		}
	})

	t.Run("not a png", func(t *testing.T) {
		if n := CountCharaChunks([]byte("nope")); n != 0 {
			t.Errorf("got %d, want 0", n)
		}
	})
}
