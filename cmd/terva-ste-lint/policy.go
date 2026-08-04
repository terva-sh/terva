package main

// The tunables live here rather than in a config file: they are policy
// decisions that belong under review, and a lint whose thresholds can be
// edited without a diff is a lint nobody trusts.
//
// Nothing in this file reproduces ASD-STE100. The rules below are our
// restatement of the writing constraints we chose to adopt; the approved-word
// dictionary is deliberately NOT here — see dictionary.go.

// maxSentenceWords is the sentence-length ceiling. STE writes procedures at 20
// and descriptive text at 25; tool text is read as instruction, so it takes the
// procedural number.
const maxSentenceWords = 20

// maxParagraphSentences caps a paragraph. Tool descriptions blow past this by
// being written as one undifferentiated block — session_inspect was 1,900
// characters of it — and the cap is what forces the split into one paragraph
// per mode.
const maxParagraphSentences = 6

// bannedTerms maps a term we have written more than one way onto the single
// spelling we settled on. This is the "one term, one meaning" rule, and it is
// the check that pays for itself: `cwd` named the same directory three ways
// across five tools before the Phase 1 conversion.
//
// The key is matched case-insensitively as a whole word.
var bannedTerms = map[string]string{
	"cwd":           "the working directory",
	"repo":          "the repository",
	"config":        "the configuration",
	"spawn":         "start",
	"e.g.":          "for example",
	"i.e.":          "that is",
	"etc.":          "a closed list, or 'and other <things>'",
	"via":           "with, or by",
	"regex":         "the regular expression",
	"dir":           "the directory",
	"param":         "the field, or the argument",
	"arg":           "the argument",
	"info":          "the information",
	"docs":          "the documentation",
	"sub-task":      "the task",
	"mutates":       "changes",
	"mutating":      "that change data",
	"idempotent":    "the tool changes nothing when you repeat the call",
	"deterministic": "constant, or always the same",
	"granular":      "detailed",
	"leverage":      "use",
	"utilize":       "use",
	"prefer":        "use",
	"reflexive":     "usual",
	"pruned":        "removed",
	"honoring":      "obeys",
	"honors":        "obeys",
	"inline":        "in your context",
	"paper over":    "hide",
	"reach for":     "use",
	"weave":         "put",
	"knob":          "the field",
	"roll off":      "archive",
	"clutter":       "the finished tasks",
	"board":         "the task list",
	"stalling":      "stopping",
	"blast radius":  "the effect",
	"footgun":       "the danger",
	"re-roll":       "convene again, or send again",
	"whilst":        "while",
	"whereas":       "but",
	"thereby":       "therefore",
	"hence":         "therefore",
	"ought":         "must",
	"shall":         "must",
	"may not":       "must not, or is permitted not to — say which",
	"lets":          "permits",
	"needs to":      "must",
	"wants to":      "must",
	"a bunch of":    "several, or a number of",
	"a couple of":   "two",
	"handful":       "several",
	"nearly always": "almost always",
	"genuinely":     "truly",
	"simply":        "delete this word",
	"basically":     "delete this word",
	"essentially":   "delete this word",
	"obviously":     "delete this word",
	"of course":     "delete this phrase",
	"keep in mind":  "remember",
	"ensure":        "make sure",
	"utilise":       "use",
	"commence":      "start",
	"terminate":     "stop",
	"prior to":      "before",
	"subsequent to": "after",
	"in order to":   "to",
	"due to":        "because of",
	"in the event":  "if",
	"a number of":   "several, or give the number",
}

// allowedINGForms are the -ing words that survive the no-participles rule:
// nouns, prepositions, and technical names that happen to end in -ing. Every
// other -ing word is a participle or a gerund and gets flagged.
//
// This list was not authored up front. It is the residue of running the lint
// over the converted corpus and keeping only the words that were genuinely not
// verb forms — the first run was the audit.
var allowedINGForms = map[string]bool{
	"string":     true,
	"strings":    true,
	"during":     true,
	"nothing":    true,
	"something":  true,
	"anything":   true,
	"everything": true,
	"setting":    true,
	"settings":   true,
	"warning":    true,
	"warnings":   true,
	"listing":    true, // a noun in the session_inspect sense
	"missing":    true, // adjectival, and the plainest available word
	"remaining":  true,
	"following":  true,
	"working":    true, // only in "the working directory", our settled term
	"logging":    true,
	"encoding":   true,
	"padding":    true,
	"mapping":    true,
	"thing":      true,
	"things":     true,
	"bring":      true,

	// Nouns and adjectives that the -ing shape catches by accident. Each of
	// these was a finding on the first run, and each one survived review as not
	// a verb form:
	"meaning":    true, // "has a meaning"
	"accounting": true, // "the cache accounting of a turn"
	"reasoning":  true, // "the reasoning effort", a field name
	"coding":     true, // "an external coding agent", adjectival
	"convening":  true, // "a convening profile", our own domain noun
	"pending":    true, // a task status value
	"wiring":     true, // "the UI wiring", a noun for the glue code itself
	"staging":    true, // "the staging clone", an environment name
}

// allowedCaps are the all-capital tokens that are names, not emphasis. STE has
// no typographic emphasis device, so a capitalised word that is NOT one of
// these is a writer shouting — which is exactly what Phase 1 removed.
var allowedCaps = map[string]bool{
	"JSON": true, "HTTP": true, "HTTPS": true, "URL": true, "URI": true,
	"RE2": true, "SHA": true, "HEAD": true, "SIGTERM": true, "SIGHUP": true,
	"CSV": true, "PNG": true, "JPG": true, "GIF": true, "WEBP": true,
	"UTF": true, "ASCII": true, "API": true, "CLI": true, "TUI": true,
	"RPC": true, "MCP": true, "SDK": true, "OAuth": true, "ID": true,
	"KB": true, "MB": true, "GB": true, "OK": true, "IO": true, "OS": true,
	// Names that first appeared with the AGENTS.md enrollment. Each one is an
	// initialism the repository uses as a proper noun, not a writer shouting:
	"ACP": true, "PR": true, "CI": true, "UI": true, "WSL": true,
	"JSONL": true, "CGO": true, "SSH": true,
	"AND": false, "OR": false, "NOT": false, // conjunctions in caps ARE emphasis
}

// beVerbs and participle detection drive the passive-voice rule.
var beVerbs = map[string]bool{
	"is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "am": true,
}

// irregularParticiples are past participles that do not end in -ed, so the
// passive check would miss them.
var irregularParticiples = map[string]bool{
	"done": true, "made": true, "given": true, "taken": true, "shown": true,
	"known": true, "held": true, "sent": true, "kept": true, "built": true,
	"left": true, "read": true, "set": true, "put": true, "cut": true,
	"written": true, "drawn": true, "thrown": true, "chosen": true,
	"found": true, "lost": true, "meant": true, "told": true, "seen": true,
	"run": true, "begun": true, "spent": true, "split": true, "hit": true,
}
