package agent

import (
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/i18n"
)

// runCardCommand dispatches `terva card ...` subcommands.
func runCardCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "card" {
		return false, nil
	}
	if len(rawArgs) == 1 {
		printCardHelp()
		return true, nil
	}
	switch rawArgs[1] {
	case "info", "show":
		return true, cardInfo(rawArgs[2:])
	case "help", "-h", "--help":
		printCardHelp()
		return true, nil
	default:
		printCardHelp()
		return true, i18n.Errorf("unknown card subcommand: %s", rawArgs[1])
	}
}

func printCardHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.card", `terva card — inspect SillyTavern character cards

usage:
  terva card info <path>...   summarize a card (.json or .png)

Load a card as a chat/play identity with --card <path> (implies --chat). A card
is data, never code: its extensions are never interpreted as capabilities, and
creator_notes is never sent to the model.`))
}

func cardInfo(args []string) error {
	if len(args) == 0 {
		return i18n.Errorf("usage: terva card info <path>...")
	}
	failed := false
	for _, path := range args {
		c, err := card.Load(path)
		if err != nil {
			fmt.Printf("✗ %s: %v\n", path, err)
			failed = true
			continue
		}
		printCardSummary(path, c)
	}
	if failed {
		return fmt.Errorf("one or more cards could not be read")
	}
	return nil
}

func printCardSummary(path string, c card.Card) {
	spec := "V1"
	if c.SpecVersion != "" {
		spec = "V" + c.SpecVersion
	}
	fmt.Println(path)
	fmt.Printf("  name:        %s\n", c.Name)
	fmt.Printf("  spec:        %s\n", spec)
	if c.Creator != "" || c.CharacterVersion != "" {
		fmt.Printf("  creator:     %s %s\n", c.Creator, versionParen(c.CharacterVersion))
	}
	if len(c.Tags) > 0 {
		fmt.Printf("  tags:        %s\n", strings.Join(c.Tags, ", "))
	}
	fmt.Printf("  description: %s\n", cardFieldSize(c.Description))
	fmt.Printf("  personality: %s\n", cardFieldSize(c.Personality))
	fmt.Printf("  scenario:    %s\n", cardFieldSize(c.Scenario))
	fmt.Printf("  first_mes:   %s\n", cardFieldSize(c.FirstMes))
	fmt.Printf("  mes_example: %s\n", cardFieldSize(c.MesExample))
	fmt.Printf("  greetings:   1 + %d alternate\n", len(c.AlternateGreetings))
	if c.SystemPrompt != "" || c.PostHistoryInstructions != "" {
		fmt.Printf("  prompting:   system_prompt %s (replaces the intro block), post_history %s (per-turn tail)\n",
			cardFieldSize(c.SystemPrompt), cardFieldSize(c.PostHistoryInstructions))
	}
	if c.CharacterBook != nil {
		// Report what will actually load, not just the authored count —
		// cardBookToLore drops disabled, empty, and unactivatable entries.
		effective, _ := cardBookToLore(c.CharacterBook, strings.TrimSpace(c.Name), "User")
		if len(effective) == len(c.CharacterBook.Entries) {
			fmt.Printf("  lorebook:    %d entries\n", len(c.CharacterBook.Entries))
		} else {
			fmt.Printf("  lorebook:    %d entries (%d load; disabled/empty/keyless entries are dropped)\n",
				len(c.CharacterBook.Entries), len(effective))
		}
	}
	if strings.TrimSpace(c.CreatorNotes) != "" {
		fmt.Printf("  creator_notes (not sent to the model):\n    %s\n", clipOneLine(c.CreatorNotes, 200))
	}
}

func cardFieldSize(s string) string {
	if s == "" {
		return "—"
	}
	return fmt.Sprintf("%d bytes (~%d tok)", len(s), len(s)/4)
}

func versionParen(v string) string {
	if v == "" {
		return ""
	}
	return "(" + v + ")"
}

func clipOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
