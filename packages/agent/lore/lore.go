// Package lore implements terva's keyed-context primitive: authored,
// file-backed entries that are injected into the model's context when they
// are keyword-relevant, within a token budget. It is the general form of a
// SillyTavern-style "World Info" / character-card lorebook — a CCv2
// character_book imports onto it. See docs/proposals/character-cards.md.
//
// The engine (Select) is pure and dependency-free: it takes entries, a
// scan window of recent messages, a budget, and a token counter, and
// returns the activated entries bucketed for placement. Discovery, trust
// gating, and prompt injection live in package agent, which composes this.
package lore

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Logic selects how an entry's secondary keys combine with a primary-key
// match. The integer values match SillyTavern's selectiveLogic enum so a
// CCv2 character_book imports without remapping.
type Logic int

const (
	LogicAndAny Logic = 0 // primary matched AND at least one secondary matches (default)
	LogicNotAll Logic = 1 // primary matched AND at least one secondary is absent
	LogicNotAny Logic = 2 // primary matched AND no secondary matches
	LogicAndAll Logic = 3 // primary matched AND every secondary matches
)

// Position places an entry relative to the character-definition block when
// its content is injected. Constant entries land in the cached prefix at
// this position; triggered entries ride the per-turn tail (where the
// distinction is moot) — see the proposal's cache-split rule.
type Position int

const (
	PositionAfter  Position = iota // after the character definition (default)
	PositionBefore                 // before the character definition
)

const (
	// DefaultScanDepth mirrors SillyTavern's world_info_depth default: the
	// number of most-recent messages scanned for keys.
	DefaultScanDepth = 2
	// DefaultOrder is the insertion order applied when an entry omits it.
	// Higher order = higher priority under budget and earlier placement.
	DefaultOrder = 100
	// maxRecursionSteps bounds the recursion fixpoint as a safety net; the
	// already-activated set is the real terminator.
	maxRecursionSteps = 100
)

// Entry is one lore item — content plus the triggers that activate it.
type Entry struct {
	Name          string
	Keys          []string // primary trigger keywords
	SecondaryKeys []string // combined with Keys via Logic when non-empty
	Logic         Logic
	Constant      bool // always active, regardless of keys
	Order         int  // priority under budget + placement order
	Position      Position
	CaseSensitive bool
	// PreventRecursion keeps this entry's content out of the recursion scan
	// buffer, so it cannot trigger further entries.
	PreventRecursion bool
	// ExcludeRecursion bars this entry from being activated by a recursion
	// pass; it can still activate on the initial scan.
	ExcludeRecursion bool
	// ScanDepth overrides the collection scan depth for this entry when
	// non-nil.
	ScanDepth *int
	Content   string
	// Source is provenance for display/logging: a file path,
	// "ext:<name>:<rel>", or "card" for a character_book import.
	Source string
}

// Config holds collection-level settings (lore.json at a directory root, or
// the book-level fields of a CCv2 character_book).
type Config struct {
	ScanDepth         int  // recent messages to scan (<=0 => DefaultScanDepth)
	TokenBudget       int  // absolute token budget; <=0 => no lore-imposed cap
	RecursiveScanning bool // re-scan activated content to trigger more entries
	CaseSensitive     bool // default case sensitivity for entries
	MatchWholeWords   bool // default whole-word matching for entries
}

// entryFrontmatter is the YAML frontmatter of a lore entry file.
type entryFrontmatter struct {
	Name             string   `yaml:"name"`
	Keys             []string `yaml:"keys"`
	SecondaryKeys    []string `yaml:"secondary_keys"`
	Logic            string   `yaml:"logic"`    // and_any|not_all|not_any|and_all
	Constant         bool     `yaml:"constant"` // always active, ignores keys
	Order            *int     `yaml:"order"`    // nil => DefaultOrder
	Position         string   `yaml:"position"` // before|after
	CaseSensitive    bool     `yaml:"case_sensitive"`
	PreventRecursion bool     `yaml:"prevent_recursion"`
	ExcludeRecursion bool     `yaml:"exclude_recursion"`
	ScanDepth        *int     `yaml:"scan_depth"`
	Enabled          *bool    `yaml:"enabled"` // nil/true => enabled
}

// ParseEntry parses one lore entry (YAML frontmatter + Markdown body).
// source is recorded as provenance. A disabled entry returns ok=false with
// no error, so discovery can skip it silently. An entry that could never
// activate (no keys and not constant) or has no content is an error.
func ParseEntry(raw, source string) (entry Entry, ok bool, err error) {
	front, body := splitFrontmatter(raw)
	var fm entryFrontmatter
	if strings.TrimSpace(front) != "" {
		if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
			return Entry{}, false, fmt.Errorf("lore %s: parse frontmatter: %w", source, err)
		}
	}
	if fm.Enabled != nil && !*fm.Enabled {
		return Entry{}, false, nil
	}
	content := strings.TrimSpace(body)
	if content == "" {
		return Entry{}, false, fmt.Errorf("lore %s: empty content", source)
	}
	keys := trimAll(fm.Keys)
	if len(keys) == 0 && !fm.Constant {
		return Entry{}, false, fmt.Errorf("lore %s: entry has no keys and is not constant (it could never activate)", source)
	}
	lg, err := parseLogic(fm.Logic)
	if err != nil {
		return Entry{}, false, fmt.Errorf("lore %s: %w", source, err)
	}
	pos, err := parsePosition(fm.Position)
	if err != nil {
		return Entry{}, false, fmt.Errorf("lore %s: %w", source, err)
	}
	order := DefaultOrder
	if fm.Order != nil {
		order = *fm.Order
	}
	return Entry{
		Name:             strings.TrimSpace(fm.Name),
		Keys:             keys,
		SecondaryKeys:    trimAll(fm.SecondaryKeys),
		Logic:            lg,
		Constant:         fm.Constant,
		Order:            order,
		Position:         pos,
		CaseSensitive:    fm.CaseSensitive,
		PreventRecursion: fm.PreventRecursion,
		ExcludeRecursion: fm.ExcludeRecursion,
		ScanDepth:        fm.ScanDepth,
		Content:          content,
		Source:           source,
	}, true, nil
}

func parseLogic(s string) (Logic, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "and_any", "and-any", "andany":
		return LogicAndAny, nil
	case "not_all", "not-all", "notall":
		return LogicNotAll, nil
	case "not_any", "not-any", "notany":
		return LogicNotAny, nil
	case "and_all", "and-all", "andall":
		return LogicAndAll, nil
	default:
		return 0, fmt.Errorf("unknown logic %q (want and_any|not_all|not_any|and_all)", s)
	}
}

func parsePosition(s string) (Position, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "after", "after_char":
		return PositionAfter, nil
	case "before", "before_char":
		return PositionBefore, nil
	default:
		return 0, fmt.Errorf("unknown position %q (want before|after)", s)
	}
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// SplitFrontmatter splits "---\n<yaml>\n---\n<body>", returning (frontmatter,
// body), or ("", raw) when no frontmatter is present.
//
// Exported for the durable-memory archive, whose files are lore entries with a
// few extra fields of their own (when they were archived, and why). It parses
// them with ParseEntry — the shared half — and needs the raw frontmatter back to
// pull its own fields, which yaml would otherwise discard as unknown. Without
// this it would need a fourth copy of a splitter the tree already has three of
// (splitPersonaFrontmatter, skills.splitFrontmatter, this).
func SplitFrontmatter(raw string) (string, string) { return splitFrontmatter(raw) }

// splitFrontmatter splits "---\n<yaml>\n---\n<body>", returning
// (frontmatter, body), or ("", raw) when no frontmatter is present. Copied
// from splitPersonaFrontmatter (persona.go) / skills.splitFrontmatter, both
// private to their packages.
func splitFrontmatter(raw string) (string, string) {
	rest := strings.TrimLeft(raw, " \t\r\n")
	if !strings.HasPrefix(rest, "---") {
		return "", raw
	}
	rest = strings.TrimPrefix(rest, "---")
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", raw // malformed; treat as no frontmatter
	}
	front := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimLeft(body, " \t\r\n")
	return front, body
}
