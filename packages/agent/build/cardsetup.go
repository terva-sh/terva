package build

import (
	"fmt"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/lore"
)

// cardIdentity is a loaded --card resolved for this run: an additive Persona
// (the card's descriptive fields as a charter, bracketed by terva's chat/play
// framing), the opening greeting to seed, and the card's character_book
// mapped onto ephemeral lore entries.
type cardIdentity struct {
	Persona       Persona
	greeting      string   // the SELECTED greeting (greetings[greetingIdx]) — the active opening
	greetings     []string // first_mes + all alternate_greetings, macro-substituted, in card order — the swipeable openings
	introOverride string   // the card's intro block (system_prompt with {{original}} resolved, else the short framing)
	introSource   string   // provenance for introOverride: "card:system_prompt" or "card:framing"
	postHistory   string   // card post_history_instructions (macros + {{original}}); "" if none
	lore          []lore.Entry
	loreCfg       lore.Config
}

// resolveCardUserName picks what a card's {{user}} macro resolves to: the --as
// flag, else the resolved user_name (a trusted project's override or the user's
// global preference — see ResolveConfig, so pass eff.Config not eff.User), else
// the literal "User". The interactive "ask once and remember" step runs earlier
// (runInteractive) and populates args.As, so Resolve itself stays non-interactive.
func resolveCardUserName(args Args, cfg config.Config) string {
	if n := strings.TrimSpace(args.As); n != "" {
		return n
	}
	if n := strings.TrimSpace(cfg.UserName); n != "" {
		return n
	}
	return "User"
}

func loadCardIdentity(path, Experience string, greetingIdx int, userName string) (cardIdentity, error) {
	c, err := card.Load(path)
	if err != nil {
		return cardIdentity{}, fmt.Errorf("--card: %w", err)
	}
	charName := strings.TrimSpace(c.Name)
	// Greeting 0 is first_mes; 1..N are the alternate_greetings.
	greetings := append([]string{c.FirstMes}, c.AlternateGreetings...)
	if greetingIdx < 0 || greetingIdx >= len(greetings) {
		return cardIdentity{}, fmt.Errorf("--greeting %d out of range: card has %d greeting(s) (0..%d)", greetingIdx, len(greetings), len(greetings)-1)
	}
	entries, cfg := CardBookToLore(c.CharacterBook, charName, userName)
	// Substitute macros in every greeting, in card order, so the whole set can be
	// pre-seeded as message-0 swipe variants; the selected one stays the active
	// opening.
	allGreetings := make([]string, 0, len(greetings))
	for _, g := range greetings {
		allGreetings = append(allGreetings, strings.TrimSpace(card.Substitute(g, charName, userName)))
	}
	ci := cardIdentity{
		Persona: Persona{
			Name:    charName,
			Charter: buildCardCharter(c, charName, userName),
			Source:  "card:" + filepath.Base(path),
		},
		greeting:  allGreetings[greetingIdx],
		greetings: allGreetings,
		lore:      entries,
		loreCfg:   cfg,
	}
	// A card owns its identity. Its system_prompt replaces terva's intro block,
	// with {{original}} resolving to a short, brand-free framing (not terva's
	// branded coding intro), and the descriptive charter + terva's conventions
	// still bracket it (terva-brackets-the-card). A card that supplies no
	// system_prompt gets that same short framing as its intro, so terva's
	// branding never leaks onto a character.
	original := experienceFraming(Experience)
	if sp := strings.TrimSpace(card.Substitute(c.SystemPrompt, charName, userName)); sp != "" {
		ci.introOverride = card.ApplyOriginal(sp, original)
		ci.introSource = "card:system_prompt"
	} else {
		ci.introOverride = original
		ci.introSource = "card:framing"
	}
	// post_history_instructions rides the per-turn tail after history; terva
	// has no default PHI, so {{original}} resolves to empty.
	if phi := strings.TrimSpace(card.Substitute(c.PostHistoryInstructions, charName, userName)); phi != "" {
		ci.postHistory = card.ApplyOriginal(phi, "")
	}
	return ci, nil
}

// buildCardCharter assembles the card's descriptive fields into an additive
// charter. Stage 1: description / personality / scenario + example dialogue
// as static text; system_prompt and post_history_instructions are Stage 2.
func buildCardCharter(c card.Card, charName, userName string) string {
	sub := func(s string) string { return strings.TrimSpace(card.Substitute(s, charName, userName)) }
	var b strings.Builder
	add := func(label, s string) {
		if s == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if label != "" {
			b.WriteString(label)
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	add("", sub(c.Description))
	add("Personality:", sub(c.Personality))
	add("Scenario:", sub(c.Scenario))
	if ex := cardExampleDialogue(c.MesExample, charName, userName); ex != "" {
		add("Example dialogue:", ex)
	}
	return b.String()
}

// cardExampleDialogue parses mes_example on <START> into clean, macro-
// substituted example blocks. terva has no example-message primitive, so
// examples fold into the cached charter (rather than being injected as
// separate messages), with the literal <START> markers stripped.
func cardExampleDialogue(mesExample, charName, userName string) string {
	var blocks []string
	for _, b := range card.ExampleBlocks(mesExample) {
		if s := strings.TrimSpace(card.Substitute(b, charName, userName)); s != "" {
			blocks = append(blocks, s)
		}
	}
	return strings.Join(blocks, "\n\n---\n\n")
}

// CardBookToLore maps a CCv2 character_book onto lore entries + config for
// this session (ephemeral, never written to disk). Macros in keys/content are
// substituted; disabled and unactivatable entries are dropped.
func CardBookToLore(book *card.CharacterBook, charName, userName string) ([]lore.Entry, lore.Config) {
	if book == nil {
		return nil, lore.Config{}
	}
	var cfg lore.Config
	if book.ScanDepth != nil {
		cfg.ScanDepth = *book.ScanDepth
	}
	if book.TokenBudget != nil {
		cfg.TokenBudget = *book.TokenBudget
	}
	if book.RecursiveScanning != nil {
		cfg.RecursiveScanning = *book.RecursiveScanning
	}
	var out []lore.Entry
	for _, be := range book.Entries {
		if be.Enabled != nil && !*be.Enabled {
			continue
		}
		content := strings.TrimSpace(card.Substitute(be.Content, charName, userName))
		if content == "" {
			continue
		}
		keys := substituteKeys(be.Keys, charName, userName)
		if len(keys) == 0 && !be.Constant {
			continue
		}
		pos := lore.PositionAfter
		if be.Position == "before_char" {
			pos = lore.PositionBefore
		}
		name := be.Name
		if name == "" {
			name = be.Comment
		}
		// Per CCv2, secondary_keys gate activation only when `selective` is
		// set — ST serializes leftover keysecondary even after selective is
		// toggled off, and the lore engine treats ANY non-empty SecondaryKeys
		// as a mandatory gate.
		var secondary []string
		if be.Selective {
			secondary = substituteKeys(be.SecondaryKeys, charName, userName)
		}
		out = append(out, lore.Entry{
			Name:          name,
			Keys:          keys,
			SecondaryKeys: secondary,
			Logic:         lore.LogicAndAny, // CCv2 selective default
			Constant:      be.Constant,
			Order:         be.InsertionOrder,
			Position:      pos,
			CaseSensitive: be.CaseSensitive,
			Content:       content,
			Source:        "card",
		})
	}
	return out, cfg
}

// mergeCardLoreConfig folds a card's book-level lore config over the config a
// lore.json tier discovered: the card is PRIMARY for the fields it sets
// (scan_depth, token_budget — the proposal's card-budget-primary rule), with
// the file config only filling what the card leaves unset. RecursiveScanning
// is OR'd — the CCv2 *bool tri-state is lost at import, so a card can enable
// recursion but cannot veto a tier that enabled it.
func mergeCardLoreConfig(base, card lore.Config) lore.Config {
	if card.ScanDepth != 0 {
		base.ScanDepth = card.ScanDepth
	}
	if card.TokenBudget != 0 {
		base.TokenBudget = card.TokenBudget
	}
	if card.RecursiveScanning {
		base.RecursiveScanning = true
	}
	return base
}

func substituteKeys(keys []string, charName, userName string) []string {
	var out []string
	for _, k := range keys {
		if s := strings.TrimSpace(card.Substitute(k, charName, userName)); s != "" {
			out = append(out, s)
		}
	}
	return out
}
