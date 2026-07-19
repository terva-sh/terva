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

	// SpecVersion is the card's declared spec_version ("2.0", "3.0", or ""
	// for a V1 card). Informational.
	SpecVersion string `json:"-"`
}

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

// Marshal encodes a Card as a CCv2 character-card JSON document
// ({spec, spec_version, data}) that ParseJSON round-trips. Unknown `extensions`
// ride through verbatim (json.RawMessage), honoring the round-trip rule the
// character-card design requires. The output is deterministic for a given Card,
// so it is safe to content-hash for a stable library id.
func Marshal(c Card) ([]byte, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	doc := struct {
		Spec        string          `json:"spec"`
		SpecVersion string          `json:"spec_version"`
		Data        json.RawMessage `json:"data"`
	}{Spec: "chara_card_v2", SpecVersion: "2.0", Data: data}
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
