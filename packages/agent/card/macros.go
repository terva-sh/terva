package card

import (
	"regexp"
	"strings"
)

var (
	reCharMacro     = regexp.MustCompile(`(?i)\{\{char\}\}|<BOT>|<CHAR>`)
	reUserMacro     = regexp.MustCompile(`(?i)\{\{user\}\}|<USER>`)
	reOriginalMacro = regexp.MustCompile(`(?i)\{\{original\}\}`)
)

// Substitute replaces the {{char}} / {{user}} placeholders (and the legacy
// <BOT>/<CHAR>/<USER> forms), case-insensitively, with the character and
// user names. Replacement is literal — the names are never treated as a
// regex template.
func Substitute(s, char, user string) string {
	if s == "" {
		return s
	}
	s = reCharMacro.ReplaceAllLiteralString(s, char)
	s = reUserMacro.ReplaceAllLiteralString(s, user)
	return s
}

// ApplyOriginal resolves the {{original}} macro used in system_prompt /
// post_history_instructions: the first occurrence becomes original, any
// further occurrence becomes empty — mirroring SillyTavern's one-shot macro.
func ApplyOriginal(s, original string) string {
	if s == "" {
		return s
	}
	first := true
	return reOriginalMacro.ReplaceAllStringFunc(s, func(string) string {
		if first {
			first = false
			return original
		}
		return ""
	})
}

var reStartDelim = regexp.MustCompile(`(?i)<START>`)

// ExampleBlocks splits mes_example on the SillyTavern <START> delimiter into
// individual example-conversation blocks (trimmed, empties dropped). Macro
// substitution is the caller's job (it knows the char/user names).
func ExampleBlocks(mesExample string) []string {
	if strings.TrimSpace(mesExample) == "" {
		return nil
	}
	var out []string
	for _, b := range reStartDelim.Split(mesExample, -1) {
		if t := strings.TrimSpace(b); t != "" {
			out = append(out, t)
		}
	}
	return out
}
