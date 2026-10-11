// Package resultcap bounds the text of one tool result and keeps the rest in a
// spill file that the model can page with the read tool.
//
// The built-in bash and read tools bound their own output. A tool that relays a
// third party's output does not: an MCP server or an extension can return
// megabytes of text in one call, up to the 4 MiB frame limit of its wire, and
// one such call can fill the context window. Cap is the bound those relays
// share, at the limit bash and read already use.
//
// 🔑 This is a leaf under tools, not part of tools. The mcp package calls Cap,
// and tools imports config, which imports mcp, so mcp cannot import tools.
//
// The spill file goes to the OS temp directory beside the bash spill, never
// under $TERVA_HOME/sessions: the read tool refuses that tree, and a pointer the
// model cannot follow is worse than none.
package resultcap

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// MaxBytes is the most text one tool result carries inline. It is the limit
// the bash and read tools use, so a relayed result costs no more context than
// a built-in one.
const MaxBytes = 50 * 1024

// Cap bounds the text of content to MaxBytes. Content at or under the limit
// comes back unchanged with an empty path.
//
// Over the limit, Cap writes every text block to a spill file, one newline
// between blocks, and returns the content cut to MaxBytes of text with a
// pointer block appended. The pointer names the file and the line the cut fell
// on, so the model can read the rest with offset and limit. Image blocks keep
// their place and do not count toward the limit.
//
// kind names the source in the file name, as in terva-<kind>-<hex>.log. path is
// the spill file, or "" when nothing was cut or the spill failed. A failed
// spill still cuts, because the bound is what protects the context window, and
// the pointer then says the rest was not saved.
func Cap(content []provider.Content, kind string) (out []provider.Content, path string) {
	total := 0
	for _, c := range content {
		if tb, ok := c.(provider.TextBlock); ok {
			total += len(tb.Text)
		}
	}
	if total <= MaxBytes {
		return content, ""
	}

	spill := NewSpillWriter(kind)
	out = make([]provider.Content, 0, len(content)+1)
	budget := MaxBytes
	line := 1 // the spill-file line that holds the first byte left out
	cut, first := false, true
	for _, c := range content {
		tb, ok := c.(provider.TextBlock)
		if !ok {
			out = append(out, c)
			continue
		}
		if !first {
			spill.Write([]byte("\n"))
			if !cut {
				line++
			}
		}
		first = false
		spill.Write([]byte(tb.Text))
		if cut {
			continue
		}
		if len(tb.Text) <= budget {
			out = append(out, tb)
			budget -= len(tb.Text)
			line += strings.Count(tb.Text, "\n")
			continue
		}
		n := cutPoint(tb.Text, budget)
		if n > 0 {
			out = append(out, provider.TextBlock{Text: tb.Text[:n]})
		}
		line += strings.Count(tb.Text[:n], "\n")
		cut = true
	}
	spill.Close()
	path = spill.Path()
	if path == "" {
		spill.Discard() // a write that failed part way leaves a partial file
	}
	return append(out, provider.TextBlock{Text: pointer(path, total, line)}), path
}

// cutPoint returns how many bytes of s to keep within budget. It prefers the
// end of a line in the second half of the budget, so the model sees whole lines
// and the continuation line is exact. A long single line, the usual shape of a
// JSON result, falls back to the budget itself, moved back to a rune start so
// the kept text stays valid UTF-8.
func cutPoint(s string, budget int) int {
	if budget <= 0 {
		return 0
	}
	if budget >= len(s) {
		return len(s)
	}
	if i := strings.LastIndexByte(s[:budget], '\n'); i >= 0 && i+1 >= budget/2 {
		return i + 1
	}
	n := budget
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// pointer is the note Cap appends after a cut. It mirrors the bash footer: it
// names the full output and tells the model to page that file with read rather
// than call the tool again, which would only cut the same result again. The
// offset is a line, because read pages by line, and a byte count here is what
// a model mistakes for an offset. The path ends its own line, because a period
// after it gets copied into the read call.
func pointer(path string, total, line int) string {
	kib := (total + 1023) / 1024
	if path == "" {
		return i18n.P("toolresult.cut.nospill",
			"[The result stops at the %d KiB limit. The full result has %d KiB. terva could not save the rest to a file. To get the rest, call the tool again and ask for less.]",
			MaxBytes/1024, kib)
	}
	return i18n.P("toolresult.cut",
		"[The result stops at the %d KiB limit. The full result has %d KiB.\nfull output: %s\nRead this file with offset and limit instead of another call to the tool. Continue with offset=%d.]",
		MaxBytes/1024, kib, path, line)
}

// SpillWriter tees a tool's complete output to a temp file as it arrives, so a
// "full output" pointer names the genuine, uncut output without holding it all
// in memory. The file is created lazily on the first byte, so a tool with no
// output never touches the disk, and Discard removes it when the inline view
// was complete. The bash tool streams into one, and Cap writes one in a pass.
type SpillWriter struct {
	kind string
	f    *os.File
	path string
	err  error
}

// NewSpillWriter returns a writer whose file will be named
// terva-<kind>-<hex>.log in the OS temp directory, with mode 0600.
func NewSpillWriter(kind string) *SpillWriter { return &SpillWriter{kind: kind} }

// Write appends a chunk, creating the backing file on first use. Errors are
// latched and silently swallow further writes. A failed spill must not break
// the tool, and the inline (capped) output still stands.
func (w *SpillWriter) Write(p []byte) {
	if w.err != nil {
		return
	}
	if w.f == nil {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		name := filepath.Join(os.TempDir(), "terva-"+w.kind+"-"+hex.EncodeToString(b)+".log")
		f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			w.err = err
			return
		}
		w.f, w.path = f, name
	}
	if _, err := w.f.Write(p); err != nil {
		w.err = err
	}
}

// Close flushes and closes the backing file (if any).
func (w *SpillWriter) Close() {
	if w.f != nil {
		_ = w.f.Close()
	}
}

// Path returns the spill file path, or "" if nothing was written or the spill
// failed.
func (w *SpillWriter) Path() string {
	if w.err != nil {
		return ""
	}
	return w.path
}

// Discard removes the spill file. Used when the inline output was complete, so
// there is no "full output" worth keeping.
func (w *SpillWriter) Discard() {
	if w.path != "" {
		_ = os.Remove(w.path)
		w.path = ""
	}
}
