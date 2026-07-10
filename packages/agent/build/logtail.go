package build

// Shared log-tail reading for the /extensions and /mcp dialogs: a full
// tail for the in-TUI log viewer, and a one-line "why it's off" reason for
// the row detail. Extension logs live at $TERVA_HOME/logs/ext-<name>.log,
// MCP server logs at mcp-<name>.log (kind is "ext" or "mcp").

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/config"
)

// logPathFor returns the log file path for an extension ("ext") or MCP
// server ("mcp") by name.
func logPathFor(kind, name string) string {
	return filepath.Join(config.LogsPath(), kind+"-"+name+".log")
}

// logTailLines returns the last maxLines lines of path (reading at most the
// trailing 128 KiB so a huge log never loads whole), trailing newline and
// \r stripped. nil when the file is missing/unreadable.
func logTailLines(path string, maxLines int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	const maxBytes = 128 * 1024
	var start int64
	if info.Size() > maxBytes {
		start = info.Size() - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	text := string(data)
	if start > 0 {
		// Dropped into the middle of a line; discard the partial head.
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

// ReadLogTail returns the tail the in-TUI log viewer displays.
func ReadLogTail(kind, name string) []string {
	return logTailLines(logPathFor(kind, name), 1000)
}

func capLine(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
