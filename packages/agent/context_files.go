package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// maxContextFileBytes caps a single startup context file. Oversize files
	// error rather than truncate: silently dropping part of an instruction
	// file is worse than a clear failure.
	maxContextFileBytes = 256 * 1024
	// maxContextTotalBytes caps the combined size of all startup context
	// files. The system prompt is the cached request prefix on every turn,
	// so this guards against blowing it up.
	maxContextTotalBytes = 1024 * 1024
)

// readStartupContextFiles loads the explicit startup context files and renders
// them as one labeled block for the system prompt, or "" when there are none.
//
// configFiles come from EffectiveConfig.ContextFiles (already absolute);
// flagFiles come from --context-file and resolve against cwd. Order is
// configFiles then flagFiles; duplicates (by absolute path) keep their first
// occurrence — mirroring readAgentsContext.
//
// These are fail-fast: a missing, oversize, unreadable, or non-regular entry
// returns an error. The user named the file explicitly (in config or on the
// command line), so a problem is almost always a typo worth surfacing rather
// than silently running without the context they intended.
func readStartupContextFiles(cwd string, configFiles, flagFiles []string) (string, error) {
	paths := make([]string, 0, len(configFiles)+len(flagFiles))
	paths = append(paths, configFiles...) // already absolute
	for _, p := range flagFiles {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		paths = append(paths, filepath.Clean(p))
	}
	if len(paths) == 0 {
		return "", nil
	}

	type ctxFile struct{ path, content string }
	var files []ctxFile
	seen := map[string]bool{}
	total := 0
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true

		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("context file %q: %w", path, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("context file %q: is a directory, not a file", path)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("context file %q: not a regular file", path)
		}
		if info.Size() > maxContextFileBytes {
			return "", fmt.Errorf("context file %q: %d bytes exceeds the %d byte per-file limit; use a skill or a smaller file", path, info.Size(), maxContextFileBytes)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("context file %q: %w", path, err)
		}
		content := strings.TrimSpace(string(raw))
		if content == "" {
			continue // skip empty / whitespace-only after a successful read
		}
		total += len(content)
		if total > maxContextTotalBytes {
			return "", fmt.Errorf("startup context files exceed the %d byte total limit; load fewer or smaller files", maxContextTotalBytes)
		}
		files = append(files, ctxFile{path: path, content: content})
	}
	if len(files) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("Startup context files. Treat these as additional project/task context. Later files may refine or override earlier ones.\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "\n## %s\n\n%s\n", f.path, f.content)
	}
	return strings.TrimSpace(sb.String()), nil
}
