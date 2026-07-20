// Package card parses SillyTavern Character Card V2 (and V1) files — plain
// JSON or PNG with the JSON embedded in a `chara` text chunk — into a Card.
// It is the import layer for terva's --chat/--play card support: a Card is
// data, never code (its `extensions` object is retained verbatim but never
// interpreted as capabilities), and `creator_notes` is never sent to the
// model. See docs/proposals/character-cards.md.
package card

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Card is a parsed Character Card. V1 cards are upgraded to this shape on
// load. Field names follow the CCv2 `data` object.
type Card struct {
	Name                    string         `json:"name"`
	Description             string         `json:"description"`
	Personality             string         `json:"personality"`
	Scenario                string         `json:"scenario"`
	FirstMes                string         `json:"first_mes"`
	MesExample              string         `json:"mes_example"`
	CreatorNotes            string         `json:"creator_notes"` // human-only; never sent to the model
	SystemPrompt            string         `json:"system_prompt"`
	PostHistoryInstructions string         `json:"post_history_instructions"`
	AlternateGreetings      []string       `json:"alternate_greetings"`
	CharacterBook           *CharacterBook `json:"character_book"`
	Tags                    []string       `json:"tags"`
	Creator                 string         `json:"creator"`
	CharacterVersion        string         `json:"character_version"`
	// Extensions is retained verbatim for round-trip/inspection but is NEVER
	// interpreted as terva capabilities (tools, MCP, hooks, authority).
	Extensions json.RawMessage `json:"extensions,omitempty"`

	// --- CCv3 additions ---
	//
	// These are modelled rather than left to fall on the floor. A V3 `data`
	// object was previously unmarshalled into a struct that had no such fields,
	// so every one of them was silently dropped — and then Marshal re-emitted
	// the card as chara_card_v2, persisting the loss. They carry `omitempty` so
	// a V2 card does not grow V3 keys on round-trip.
	//
	// All are inert data. Nothing here is interpreted as a terva capability,
	// exactly as with Extensions.
	Nickname                 string            `json:"nickname,omitempty"`
	CreatorNotesMultilingual map[string]string `json:"creator_notes_multilingual,omitempty"`
	Source                   []string          `json:"source,omitempty"`
	GroupOnlyGreetings       []string          `json:"group_only_greetings,omitempty"`
	CreationDate             *int64            `json:"creation_date,omitempty"`
	ModificationDate         *int64            `json:"modification_date,omitempty"`
	// Assets are the V3 portrait/background/emotion set. terva serves a single
	// avatar today, so these ride through for fidelity and are surfaced as an
	// import warning rather than acted on.
	Assets []Asset `json:"assets,omitempty"`

	// SpecVersion is the card's declared spec_version ("2.0", "3.0", or ""
	// for a V1 card). Informational, and the switch Marshal reads to decide
	// which spec document to emit.
	SpecVersion string `json:"-"`
}

// Asset is one CCv3 `assets` entry: a typed, named media reference (icon,
// background, user_icon, emotion, or an x_-prefixed custom type). The uri is an
// http(s) URL, a base64 data URL, `embeded://path` (into a .charx container), or
// `ccdefault:` meaning "the card's own image".
type Asset struct {
	Type string `json:"type"`
	URI  string `json:"uri"`
	Name string `json:"name"`
	Ext  string `json:"ext"`
}

// IsV3 reports whether the card declared the CCv3 spec. Marshal and WritePNG
// key on this to avoid re-emitting a V3 card as a V2 one.
func (c Card) IsV3() bool { return strings.HasPrefix(c.SpecVersion, "3") }

// CharacterBook is a CCv2 embedded lorebook. terva imports it onto the lore
// engine (an ephemeral, in-memory collection for the session).
type CharacterBook struct {
	Name              string      `json:"name"`
	Description       string      `json:"description"`
	ScanDepth         *int        `json:"scan_depth"`
	TokenBudget       *int        `json:"token_budget"`
	RecursiveScanning *bool       `json:"recursive_scanning"`
	Entries           []BookEntry `json:"entries"`
}

// BookEntry is one lorebook entry (CCv2 character_book.entries[]).
type BookEntry struct {
	Keys           []string `json:"keys"`
	SecondaryKeys  []string `json:"secondary_keys"`
	Comment        string   `json:"comment"`
	Content        string   `json:"content"`
	Constant       bool     `json:"constant"`
	Selective      bool     `json:"selective"`
	InsertionOrder int      `json:"insertion_order"`
	Enabled        *bool    `json:"enabled"` // nil => enabled
	Position       string   `json:"position"`
	CaseSensitive  bool     `json:"case_sensitive"`
	Name           string   `json:"name"`
	Priority       *int     `json:"priority"`
	// UseRegex is a CCv3 addition: keys are regular expressions rather than
	// literals. Carried so it round-trips; the lore engine does NOT honor it
	// yet, which is why a card that sets it earns an import warning.
	UseRegex bool `json:"use_regex,omitempty"`
}

// pngSignature is the 8-byte PNG magic.
const pngSignature = "\x89PNG\r\n\x1a\n"

// IsPNG reports whether data begins with the PNG signature — i.e. a card that
// carries an avatar image, with its JSON in a `chara` text chunk.
func IsPNG(data []byte) bool {
	return len(data) >= len(pngSignature) && string(data[:len(pngSignature)]) == pngSignature
}

// Load reads a card from a .json or .png path, sniffing the format by
// content (PNG signature) rather than extension.
func Load(path string) (Card, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Card{}, err
	}
	if IsPNG(data) {
		jsonBytes, err := ReadPNG(data)
		if err != nil {
			return Card{}, fmt.Errorf("%s: %w", path, err)
		}
		return ParseJSON(jsonBytes)
	}
	return ParseJSON(data)
}

// LoadBytes parses a card from raw bytes, sniffing PNG-vs-JSON by content the
// same way Load does for a path. The PNG pixels are NOT retained here — only the
// embedded card JSON — so a caller that wants to keep the avatar must hold the
// original bytes itself (the library store does exactly this).
func LoadBytes(data []byte) (Card, error) {
	if IsPNG(data) {
		jsonBytes, err := ReadPNG(data)
		if err != nil {
			return Card{}, err
		}
		return ParseJSON(jsonBytes)
	}
	return ParseJSON(data)
}

// Marshal encodes a Card as a character-card JSON document
// ({spec, spec_version, data}) that ParseJSON round-trips. Unknown `extensions`
// ride through verbatim (json.RawMessage), honoring the round-trip rule the
// character-card design requires. The output is deterministic for a given Card,
// so it is safe to content-hash for a stable library id.
//
// The document keeps the SPEC THE CARD ARRIVED AS. This used to be hardcoded to
// chara_card_v2, which meant importing a V3 card wrote a V2 card.json — the V3
// fields had already been dropped by the parse, and the downgrade then made that
// loss permanent and invisible. A V3 card now stays V3 on disk.
func Marshal(c Card) ([]byte, error) {
	if c.IsV3() {
		return marshalDoc(c, "chara_card_v3", "3.0")
	}
	return marshalDoc(c, "chara_card_v2", "2.0")
}

// MarshalV2 encodes a Card as a chara_card_v2 document regardless of the spec it
// arrived as — the deliberate downgrade, for the back-compat `chara` chunk that
// rides alongside `ccv3` in an exported PNG so a V2-only reader still sees a
// card. V3-only fields are omitted rather than smuggled under a V2 spec label.
func MarshalV2(c Card) ([]byte, error) {
	v2 := c
	v2.SpecVersion = "2.0"
	v2.Nickname = ""
	v2.CreatorNotesMultilingual = nil
	v2.Source = nil
	v2.GroupOnlyGreetings = nil
	v2.CreationDate = nil
	v2.ModificationDate = nil
	v2.Assets = nil
	return marshalDoc(v2, "chara_card_v2", "2.0")
}

func marshalDoc(c Card, spec, version string) ([]byte, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	doc := struct {
		Spec        string          `json:"spec"`
		SpecVersion string          `json:"spec_version"`
		Data        json.RawMessage `json:"data"`
	}{Spec: spec, SpecVersion: version, Data: data}
	return json.MarshalIndent(doc, "", "  ")
}

// ParseJSON parses a card from JSON bytes, detecting V2 (spec + data
// wrapper) vs a flat V1 object and upgrading V1 to the V2 shape.
func ParseJSON(data []byte) (Card, error) {
	var probe struct {
		Spec        string          `json:"spec"`
		SpecVersion string          `json:"spec_version"`
		Data        json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return Card{}, fmt.Errorf("card: invalid JSON: %w", err)
	}
	var c Card
	if len(probe.Data) > 0 && strings.HasPrefix(probe.Spec, "chara_card_v") {
		if err := json.Unmarshal(probe.Data, &c); err != nil {
			return Card{}, fmt.Errorf("card: invalid data object: %w", err)
		}
		c.SpecVersion = probe.SpecVersion
	} else {
		// V1: a flat object with the six mandatory fields.
		if err := json.Unmarshal(data, &c); err != nil {
			return Card{}, fmt.Errorf("card: invalid V1 card: %w", err)
		}
	}
	if strings.TrimSpace(c.Name) == "" {
		return Card{}, fmt.Errorf("card: missing required 'name'")
	}
	return c, nil
}

// UnsupportedV3Features lists the CCv3 capabilities a card declares that terva
// carries but does not yet ACT on, phrased for a user rather than a log.
//
// The point is to make the gap visible instead of silent. A V3 card now imports
// with its data intact and round-trips unharmed, but "the fields survived" is
// not the same as "the behaviour works": a lorebook whose keys are regexes, or
// whose entries steer placement with @@decorators, will simply not match the way
// its author intended, and an emotion/background asset set is not something the
// library renders. Reporting that up front is the difference between a card that
// behaves oddly for unknown reasons and one whose limits were stated on arrival.
//
// Returns nil for a V2 card, and for a V3 card that uses nothing beyond what
// terva already honors.
func UnsupportedV3Features(c Card) []string {
	var out []string
	if c.CharacterBook != nil {
		regex, decorated := 0, 0
		for _, e := range c.CharacterBook.Entries {
			if e.UseRegex {
				regex++
			}
			if hasDecorator(e.Content) {
				decorated++
			}
		}
		if regex > 0 {
			out = append(out, fmt.Sprintf("%d lorebook %s use regex keys (use_regex), which terva matches literally for now", regex, plural(regex, "entry", "entries")))
		}
		if decorated > 0 {
			out = append(out, fmt.Sprintf("%d lorebook %s carry @@decorators, which terva keeps but does not act on yet", decorated, plural(decorated, "entry", "entries")))
		}
	}
	// The spec's default when assets is absent is a single `ccdefault:` icon —
	// i.e. the card's own image, which is exactly what terva already serves. Only
	// a set that goes beyond that is worth mentioning.
	if extra := len(nonDefaultAssets(c.Assets)); extra > 0 {
		out = append(out, fmt.Sprintf("the card defines %d extra %s (portraits, backgrounds, emotions); terva shows the main image only", extra, plural(extra, "asset", "assets")))
	}
	if len(c.GroupOnlyGreetings) > 0 {
		out = append(out, fmt.Sprintf("%d group-chat %s stored but unused (terva has no group chat)", len(c.GroupOnlyGreetings), plural(len(c.GroupOnlyGreetings), "greeting", "greetings")))
	}
	return out
}

// hasDecorator reports whether a lorebook entry's content opens any line with
// the CCv3 `@@decorator` marker. Decorators are line-leading by construction, so
// a mid-sentence "@@" in prose is not mistaken for one.
func hasDecorator(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "@@") {
			return true
		}
	}
	return false
}

// nonDefaultAssets drops the entries terva already satisfies: the spec's default
// main icon resolving to the card's own image (`ccdefault:`).
func nonDefaultAssets(assets []Asset) []Asset {
	var out []Asset
	for _, a := range assets {
		if a.Type == "icon" && a.Name == "main" && (a.URI == "ccdefault:" || a.URI == "") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
