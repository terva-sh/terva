package tui

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// HighlightCode syntax-colors src using this theme's syntax palette and
// returns the result split into lines, ready for a line-based diff
// renderer. If no language is given or chroma has no lexer for it, src
// is returned as-is (one entry per line). Safe to call from multiple
// goroutines.
//
// Results are memoised by (lang, src) so repeated calls from the view
// builder (which runs on every redraw) don't re-tokenise. Cache is
// bounded and evicts oldest entries past its cap.
func (th Theme) HighlightCode(src, lang string) []string {
	styleKey := th.syntaxKey()
	if out, ok := highlightCache.lookup(styleKey, lang, src); ok {
		return out
	}
	lexer := chooseLexer(lang)
	if lexer == nil {
		out := strings.Split(src, "\n")
		highlightCache.store(styleKey, lang, src, out)
		return out
	}
	style := th.chromaStyle()
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return strings.Split(src, "\n")
	}

	iterator, err := lexer.Tokenise(nil, src)
	if err != nil {
		out := strings.Split(src, "\n")
		highlightCache.store(styleKey, lang, src, out)
		return out
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		out := strings.Split(src, "\n")
		highlightCache.store(styleKey, lang, src, out)
		return out
	}
	out := strings.Split(strings.TrimRight(stripANSIBackgrounds(buf.String()), "\n"), "\n")
	highlightCache.store(styleKey, lang, src, out)
	return out
}

// highlightResultCache is a simple LRU-ish cache: when it exceeds its
// capacity we drop half of the entries (the oldest, based on insertion
// order). That's good enough since tool_result text doesn't change once
// emitted, so cache hits are frequent and evictions rare.
type highlightResultCache struct {
	mu    sync.Mutex
	max   int
	data  map[string][]string
	order []string
}

func (c *highlightResultCache) key(styleKey, lang, src string) string {
	return styleKey + "\x00" + lang + "\x00" + src
}

func (c *highlightResultCache) lookup(styleKey, lang, src string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[c.key(styleKey, lang, src)]
	return v, ok
}

func (c *highlightResultCache) store(styleKey, lang, src string, out []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		c.data = make(map[string][]string)
	}
	k := c.key(styleKey, lang, src)
	if _, ok := c.data[k]; !ok {
		c.order = append(c.order, k)
	}
	c.data[k] = out
	if c.max > 0 && len(c.order) > c.max {
		// Evict the oldest half so we amortise the cost.
		cut := len(c.order) / 2
		for _, old := range c.order[:cut] {
			delete(c.data, old)
		}
		c.order = append([]string(nil), c.order[cut:]...)
	}
}

var highlightCache = &highlightResultCache{max: 512}

// LanguageFromPath maps file extensions to chroma lexer names.
func LanguageFromPath(p string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(p)), ".")
	base := strings.ToLower(filepath.Base(p))
	if lang := filenameLang[base]; lang != "" {
		return lang
	}
	return extLang[ext]
}

// stripANSIBackgrounds removes SGR background-color attributes emitted by
// terminal syntax formatters while preserving foreground colors and styles.
// Chroma's inherited styles can assign black backgrounds to a few tokens
// (notably punctuation/error-ish spans), which looks like random black blocks
// inside terva's dark-gray tool boxes. Foreground color is enough for code.
func stripANSIBackgrounds(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			out.WriteByte(s[i])
			i++
			continue
		}
		j := i + 2
		for j < len(s) && s[j] != 'm' {
			j++
		}
		if j >= len(s) {
			out.WriteString(s[i:])
			break
		}
		params := strings.Split(s[i+2:j], ";")
		kept := make([]string, 0, len(params))
		for p := 0; p < len(params); p++ {
			param := params[p]
			switch param {
			case "48":
				// 48;5;N or 48;2;R;G;B background color.
				if p+1 < len(params) && params[p+1] == "5" {
					p += 2
					continue
				}
				if p+1 < len(params) && params[p+1] == "2" {
					p += 4
					continue
				}
				continue
			case "49":
				continue
			}
			// 40-47 and 100-107 are ANSI background colors.
			if len(param) == 2 && param[0] == '4' && param[1] >= '0' && param[1] <= '7' {
				continue
			}
			if len(param) == 3 && param[0] == '1' && param[1] == '0' && param[2] >= '0' && param[2] <= '7' {
				continue
			}
			kept = append(kept, param)
		}
		if len(kept) > 0 {
			out.WriteString("\x1b[")
			out.WriteString(strings.Join(kept, ";"))
			out.WriteByte('m')
		}
		i = j + 1
	}
	return out.String()
}

// chooseLexer picks the best lexer for a language hint; falls back to
// nil (no highlighting) if the hint is empty or unknown.
func chooseLexer(lang string) chroma.Lexer {
	if lang == "" {
		return nil
	}
	if l := lexers.Get(lang); l != nil {
		return chroma.Coalesce(l)
	}
	// Chroma accepts aliases; try common spellings.
	if l := lexers.Get(strings.ToLower(lang)); l != nil {
		return chroma.Coalesce(l)
	}
	return nil
}

// chromaStyle builds a syntax style from the theme. Falls back to
// chroma's bundled fallback style if the configured base style or
// overrides cannot be built.
func (th Theme) chromaStyle() *chroma.Style {
	base := th.SyntaxBaseStyle
	if base == "" {
		base = "monokai"
	}
	style := styles.Get(base)
	if style == nil {
		style = styles.Fallback
	}
	builder := style.Builder()
	add := func(tt chroma.TokenType, entry string) {
		if strings.TrimSpace(entry) != "" {
			builder.Add(tt, entry)
		}
	}
	syn := th.Syntax
	add(chroma.Keyword, syn.Keyword)
	add(chroma.KeywordConstant, syn.KeywordConstant)
	add(chroma.KeywordDeclaration, syn.KeywordDeclaration)
	add(chroma.KeywordNamespace, syn.KeywordNamespace)
	add(chroma.KeywordReserved, syn.KeywordReserved)
	add(chroma.KeywordType, syn.KeywordType)
	add(chroma.NameBuiltin, syn.NameBuiltin)
	add(chroma.NameFunction, syn.NameFunction)
	add(chroma.NameClass, syn.NameClass)
	add(chroma.NameDecorator, syn.NameDecorator)
	add(chroma.LiteralString, syn.LiteralString)
	add(chroma.LiteralStringEscape, syn.LiteralStringEscape)
	add(chroma.LiteralNumber, syn.LiteralNumber)
	add(chroma.Comment, syn.Comment)
	add(chroma.CommentPreproc, syn.CommentPreproc)
	add(chroma.Operator, syn.Operator)
	add(chroma.Punctuation, syn.Punctuation)
	add(chroma.Text, syn.Text)
	builder.Add(chroma.Background, " bg:")

	s, err := builder.Build()
	if err != nil {
		return styles.Fallback
	}
	return s
}

func (th Theme) syntaxKey() string {
	syn := th.Syntax
	return strings.Join([]string{
		th.SyntaxBaseStyle,
		syn.Keyword,
		syn.KeywordConstant,
		syn.KeywordDeclaration,
		syn.KeywordNamespace,
		syn.KeywordReserved,
		syn.KeywordType,
		syn.NameBuiltin,
		syn.NameFunction,
		syn.NameClass,
		syn.NameDecorator,
		syn.LiteralString,
		syn.LiteralStringEscape,
		syn.LiteralNumber,
		syn.Comment,
		syn.CommentPreproc,
		syn.Operator,
		syn.Punctuation,
		syn.Text,
	}, "\x1f")
}

// extLang maps file extensions to chroma lexer names. Only
// extensions that chroma supports are included.
var extLang = map[string]string{
	"ts":         "typescript",
	"tsx":        "tsx",
	"js":         "javascript",
	"jsx":        "jsx",
	"mjs":        "javascript",
	"cjs":        "javascript",
	"py":         "python",
	"rb":         "ruby",
	"rs":         "rust",
	"go":         "go",
	"java":       "java",
	"kt":         "kotlin",
	"swift":      "swift",
	"c":          "c",
	"h":          "c",
	"cpp":        "cpp",
	"cc":         "cpp",
	"cxx":        "cpp",
	"hpp":        "cpp",
	"cs":         "csharp",
	"php":        "php",
	"sh":         "bash",
	"bash":       "bash",
	"zsh":        "bash",
	"fish":       "fish",
	"ps1":        "powershell",
	"sql":        "sql",
	"html":       "html",
	"htm":        "html",
	"css":        "css",
	"scss":       "scss",
	"sass":       "sass",
	"less":       "less",
	"json":       "json",
	"yaml":       "yaml",
	"yml":        "yaml",
	"toml":       "toml",
	"xml":        "xml",
	"md":         "markdown",
	"markdown":   "markdown",
	"dockerfile": "docker",
	"makefile":   "makefile",
	"lua":        "lua",
	"perl":       "perl",
	"pl":         "perl",
	"proto":      "protobuf",
	"tf":         "terraform",
	"hcl":        "terraform",
	"graphql":    "graphql",
	"gql":        "graphql",
	"vue":        "vue",
	"svelte":     "svelte",
	"r":          "r",
	"jl":         "julia",
	"ex":         "elixir",
	"exs":        "elixir",
	"scala":      "scala",
	"nix":        "nix",
}

var filenameLang = map[string]string{
	"dockerfile":     "docker",
	"makefile":       "makefile",
	"gnumakefile":    "makefile",
	".gitconfig":     "ini",
	".gitattributes": "ini",
	"cargo.toml":     "toml",
	"go.mod":         "go-module",
	"go.sum":         "text",
	"package.json":   "json",
	"tsconfig.json":  "json",
}
