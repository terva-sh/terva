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
