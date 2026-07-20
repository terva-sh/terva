package card

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

// maxCharaBytes bounds the decoded/decompressed card JSON from an untrusted
// PNG (a zip-bomb / oversize guard). Enforced on BOTH the compressed paths
// (zlibInflate) and the uncompressed tEXt / base64 path (decodeChara). Real
// cards are a few KiB.
const maxCharaBytes = 8 << 20 // 8 MiB

// ReadPNG extracts the embedded character JSON from a PNG's character text
// chunk (tEXt, zTXt, or iTXt), decoding the common base64-of-JSON convention
// and accepting raw JSON. Reads are bounded and defensive — PNG input is
// untrusted.
//
// A CCv3 card lives in a `ccv3` chunk, and its writers ALSO emit a `chara` chunk
// holding a V2 rendering of the same character so older readers still see
// something. The spec's rule for a reader that finds both is to use `ccv3` — and
// terva used to match only `chara`, so a V3 card either imported as its
// downgraded V2 twin (quietly losing everything V3 added) or, when the writer
// emitted `ccv3` alone, failed to import at all with "no character metadata".
// Hence: `ccv3` first, `chara` as the fallback.
func ReadPNG(data []byte) ([]byte, error) {
	found, err := scanCardChunks(data)
	if err != nil {
		return nil, err
	}
	if found.ccv3 != nil {
		return decodeChara(found.ccv3)
	}
	if found.chara != nil {
		return decodeChara(found.chara)
	}
	return nil, fmt.Errorf("no character metadata (`ccv3` or `chara` chunk) in PNG")
}

// cardChunks is what one pass over a PNG's text chunks turned up: the first
// payload under each recognized keyword, plus how many of each were present so
// an importer can report an ambiguous file.
type cardChunks struct {
	ccv3       []byte
	chara      []byte
	ccv3Count  int
	charaCount int
}

// scanCardChunks walks the PNG once, collecting the first `ccv3` and first
// `chara` text chunk and counting both. One pass, so precedence and the
// duplicate report cannot disagree about what the file contains.
func scanCardChunks(data []byte) (cardChunks, error) {
	var out cardChunks
	if len(data) < len(pngSignature) || string(data[:len(pngSignature)]) != pngSignature {
		return out, fmt.Errorf("not a PNG")
	}
	pos := len(pngSignature)
	for pos+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		ctype := string(data[pos+4 : pos+8])
		start := pos + 8
		if length < 0 || start+length < start || start+length > len(data) {
			return out, fmt.Errorf("truncated or malformed PNG chunk")
		}
		if kw, text, ok := parseTextChunk(ctype, data[start:start+length]); ok {
			switch {
			case isCCv3(kw):
				out.ccv3Count++
				if out.ccv3 == nil {
					out.ccv3 = text
				}
			case isChara(kw):
				out.charaCount++
				if out.chara == nil {
					out.chara = text
				}
			}
		}
		if ctype == "IEND" {
			break
		}
		pos = start + length + 4 // skip the 4-byte CRC
	}
	return out, nil
}

// parseTextChunk decodes any of PNG's three text chunk flavors to (keyword,
// text). Not a text chunk => ok=false.
func parseTextChunk(ctype string, chunk []byte) (kw string, text []byte, ok bool) {
	switch ctype {
	case "tEXt":
		return parseTEXt(chunk)
	case "zTXt":
		return parseZTXt(chunk)
	case "iTXt":
		return parseITXt(chunk)
	}
	return "", nil, false
}

func isChara(kw string) bool { return strings.EqualFold(kw, "chara") }
func isCCv3(kw string) bool  { return strings.EqualFold(kw, "ccv3") }

// CountCharaChunks reports how many `chara` text chunks a PNG carries. ReadPNG
// takes the FIRST and ignores the rest, so anything above one means the import
// silently chose between candidates.
//
// That is not a hypothetical. A real card off chub.ai (the RPG world simulation
// engine) shipped two, and they were not duplicates — different revisions of the
// same character, one with a system prompt twice the length of the other, plus a
// differing first_mes and tags. Whichever a reader picks, it gets a materially
// different character under the same name.
//
// The PNG spec permits repeated tEXt keywords, so such a file is well-formed and
// cannot simply be rejected; the V2 card spec, meanwhile, says nothing about
// which of two `chara` chunks wins, so every reader's choice is its own. (The
// SANCTIONED multi-chunk pattern uses distinct keywords — a `ccv3` chunk beside
// a `chara` one, V3 winning — which is a precedence ladder, not this.) Almost
// always a duplicate is an exporter appending a fresh chunk instead of replacing
// the stale one; terva's own WritePNG drops every existing chara chunk before
// writing exactly one, so terva never emits these.
//
// So: take the first, deterministically, and TELL the user we had to choose.
// A count, not the chunks themselves — the caller only needs to know a choice
// was made, and returning the losing payloads would invite acting on them.
// It counts within ONE keyword. A `ccv3` chunk sitting beside a `chara` one is
// the sanctioned V3 layout, not an ambiguity — different keywords with a defined
// precedence — so it is not reported here; only a repeated keyword is.
func CountCharaChunks(data []byte) int {
	found, err := scanCardChunks(data)
	if err != nil {
		return 0
	}
	// Whichever keyword actually supplied the card is the one whose duplicates
	// mattered: ReadPNG prefers ccv3, so on a V3 file the chara chunks are the
	// back-compat copies and their count is not what the user chose between.
	if found.ccv3Count > 0 {
		return found.ccv3Count
	}
	return found.charaCount
}

// WritePNG embeds cardJSON into a copy of pngData as base64 text chunks,
// REPLACING any existing character chunk — so a card edited after import exports
// its current data, not the pixels' stale embed. The image pixels are preserved
// verbatim. The inverse of ReadPNG.
//
// A V2 document is written as one `chara` chunk (the SillyTavern convention). A
// V3 document is written as a `ccv3` chunk PLUS a `chara` chunk holding a V2
// downgrade of the same card, which is the layout V3 writers use and the reason
// ReadPNG needs a precedence rule at all: the pair keeps the card openable in
// V2-only tools without the V3 reader losing anything. Writing the V3 document
// into `chara` instead would hand a V2 reader a spec label it does not know.
func WritePNG(pngData, cardJSON []byte) ([]byte, error) {
	if len(pngData) < len(pngSignature) || string(pngData[:len(pngSignature)]) != pngSignature {
		return nil, fmt.Errorf("not a PNG")
	}
	chunks := cardChunksFor(cardJSON)
	out := make([]byte, 0, len(pngData)+2*len(cardJSON)+128)
	out = append(out, pngSignature...)
	pos := len(pngSignature)
	inserted := false
	for pos+8 <= len(pngData) {
		length := int(binary.BigEndian.Uint32(pngData[pos : pos+4]))
		ctype := string(pngData[pos+4 : pos+8])
		start := pos + 8
		if length < 0 || start+length < start || start+length+4 > len(pngData) {
			return nil, fmt.Errorf("truncated or malformed PNG chunk")
		}
		end := start + length + 4 // include the 4-byte CRC
		if isCardTextChunk(ctype, pngData[start:start+length]) {
			pos = end // drop the stale chunk; the fresh one goes in before IEND
			continue
		}
		if ctype == "IEND" {
			for _, ch := range chunks {
				out = appendPNGChunk(out, "tEXt", []byte(ch.keyword+"\x00"+base64.StdEncoding.EncodeToString(ch.doc)))
			}
			inserted = true
		}
		out = append(out, pngData[pos:end]...)
		pos = end
	}
	if !inserted {
		return nil, fmt.Errorf("PNG has no IEND chunk")
	}
	return out, nil
}

// cardChunksFor decides which text chunks a document is written as: a `ccv3`
// chunk plus a V2 back-compat `chara` chunk for a V3 card, or a lone `chara`
// chunk otherwise.
//
// A document that claims V3 but cannot be re-parsed into a Card is written as-is
// under `chara` rather than failing the export — the caller handed us bytes to
// embed, and refusing to export a card because its downgrade could not be
// computed would be a worse outcome than a single-chunk PNG.
func cardChunksFor(cardJSON []byte) []struct {
	keyword string
	doc     []byte
} {
	type chunk = struct {
		keyword string
		doc     []byte
	}
	var probe struct {
		Spec string `json:"spec"`
	}
	if err := json.Unmarshal(cardJSON, &probe); err == nil && strings.EqualFold(probe.Spec, "chara_card_v3") {
		if c, err := ParseJSON(cardJSON); err == nil {
			if v2, err := MarshalV2(c); err == nil {
				return []chunk{{"ccv3", cardJSON}, {"chara", v2}}
			}
		}
	}
	return []chunk{{"chara", cardJSON}}
}

// isCardTextChunk reports whether a chunk is a text chunk keyed "chara" or
// "ccv3" — i.e. one WritePNG must drop before writing fresh ones.
func isCardTextChunk(ctype string, data []byte) bool {
	kw, _, ok := parseTextChunk(ctype, data)
	return ok && (isChara(kw) || isCCv3(kw))
}

// appendPNGChunk writes one PNG chunk (length, type, data, CRC-32 of type+data).
func appendPNGChunk(dst []byte, ctype string, data []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	dst = append(dst, n[:]...)
	dst = append(dst, ctype...)
	dst = append(dst, data...)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(ctype))
	_, _ = crc.Write(data)
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], crc.Sum32())
	return append(dst, c[:]...)
}

// parseTEXt: keyword \0 text (uncompressed).
func parseTEXt(chunk []byte) (kw string, text []byte, ok bool) {
	i := bytes.IndexByte(chunk, 0)
	if i < 0 {
		return "", nil, false
	}
	return string(chunk[:i]), chunk[i+1:], true
}

// parseZTXt: keyword \0 compression-method(1) zlib-data.
func parseZTXt(chunk []byte) (kw string, text []byte, ok bool) {
	i := bytes.IndexByte(chunk, 0)
	if i < 0 || i+2 > len(chunk) {
		return "", nil, false
	}
	dec, err := zlibInflate(chunk[i+2:])
	if err != nil {
		return "", nil, false
	}
	return string(chunk[:i]), dec, true
}

// parseITXt: keyword \0 comp-flag(1) comp-method(1) lang \0 trans-keyword \0 text.
func parseITXt(chunk []byte) (kw string, text []byte, ok bool) {
	i := bytes.IndexByte(chunk, 0)
	if i < 0 || i+3 > len(chunk) {
		return "", nil, false
	}
	keyword := string(chunk[:i])
	compFlag := chunk[i+1]
	rest := chunk[i+3:] // skip comp-method byte
	j := bytes.IndexByte(rest, 0)
	if j < 0 {
		return "", nil, false
	}
	rest = rest[j+1:] // past language tag
	k := bytes.IndexByte(rest, 0)
	if k < 0 {
		return "", nil, false
	}
	textBytes := rest[k+1:] // past translated keyword
	if compFlag == 1 {
		dec, err := zlibInflate(textBytes)
		if err != nil {
			return "", nil, false
		}
		textBytes = dec
	}
	return keyword, textBytes, true
}

// decodeChara turns a `chara` chunk's text into JSON bytes: base64-of-JSON
// (the common convention) or, failing that, raw JSON.
func decodeChara(text []byte) ([]byte, error) {
	// Bound the uncompressed / base64 path the same way zlibInflate bounds the
	// compressed paths: reject an oversize `chara` chunk before it is copied,
	// base64-decoded, or JSON-parsed. Compressed text arrives already capped.
	if len(text) > maxCharaBytes {
		return nil, fmt.Errorf("`chara` chunk too large (%d bytes exceeds %d-byte cap)", len(text), maxCharaBytes)
	}
	s := strings.TrimSpace(string(text))
	if dec, err := base64.StdEncoding.DecodeString(stripWhitespace(s)); err == nil && looksJSON(dec) {
		return dec, nil
	}
	if looksJSON([]byte(s)) {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("`chara` chunk is neither base64-JSON nor raw JSON")
}

func looksJSON(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) > 0 && (b[0] == '{' || b[0] == '[')
}

func stripWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', ' ':
			return -1
		}
		return r
	}, s)
}

func zlibInflate(b []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, maxCharaBytes))
}
