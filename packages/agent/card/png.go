package card

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// maxCharaBytes bounds the decoded/decompressed card JSON from an untrusted
// PNG (a zip-bomb / oversize guard). Enforced on BOTH the compressed paths
// (zlibInflate) and the uncompressed tEXt / base64 path (decodeChara). Real
// cards are a few KiB.
const maxCharaBytes = 8 << 20 // 8 MiB

// ReadPNG extracts the embedded character JSON from a PNG's `chara` text
// chunk (tEXt, zTXt, or iTXt), decoding the common base64-of-JSON convention
// and accepting raw JSON. Reads are bounded and defensive — PNG input is
// untrusted.
func ReadPNG(data []byte) ([]byte, error) {
	if len(data) < len(pngSignature) || string(data[:len(pngSignature)]) != pngSignature {
		return nil, fmt.Errorf("not a PNG")
	}
	pos := len(pngSignature)
	for pos+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		ctype := string(data[pos+4 : pos+8])
		start := pos + 8
		if length < 0 || start+length < start || start+length > len(data) {
			return nil, fmt.Errorf("truncated or malformed PNG chunk")
		}
		chunk := data[start : start+length]
		switch ctype {
		case "tEXt":
			if kw, text, ok := parseTEXt(chunk); ok && isChara(kw) {
				return decodeChara(text)
			}
		case "zTXt":
			if kw, text, ok := parseZTXt(chunk); ok && isChara(kw) {
				return decodeChara(text)
			}
		case "iTXt":
			if kw, text, ok := parseITXt(chunk); ok && isChara(kw) {
				return decodeChara(text)
			}
		case "IEND":
			return nil, fmt.Errorf("no character metadata (`chara` chunk) in PNG")
		}
		pos = start + length + 4 // skip the 4-byte CRC
	}
	return nil, fmt.Errorf("no character metadata (`chara` chunk) in PNG")
}

func isChara(kw string) bool { return strings.EqualFold(kw, "chara") }

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
