package workspace

import (
	"context"
	"path/filepath"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/fswalk"
)

// ListFiles serves files.list: the workspace-file listing behind a client's
// @-file picker, walked under the workspace cwd with the picker's shared
// semantics (fswalk — the same walk the in-process TUI runs locally, so an
// attached client completes over exactly the entries a local one would see).
// The client caches the result and ranks keystrokes locally, so this walks
// per fetch, not per keystroke; the fswalk caps bound the worst case.
func (w *Workspace) ListFiles(ctx context.Context, opts ctrlproto.FilesListParams) (ctrlproto.FilesListResult, error) {
	entries, truncated, err := fswalk.List(w.cwd, fswalk.Options{
		Dir:              opts.Dir,
		Recursive:        opts.Recursive,
		RespectGitignore: opts.RespectGitignore,
	})
	if err != nil {
		// A bad Dir (absolute, ".." escape) is the caller's error; an
		// unreadable directory is not worth distinguishing for a picker.
		return ctrlproto.FilesListResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "files.list: %v", err)
	}
	res := ctrlproto.FilesListResult{
		Files:     make([]ctrlproto.FileEntry, 0, len(entries)),
		Truncated: truncated,
	}
	for _, e := range entries {
		res.Files = append(res.Files, ctrlproto.FileEntry{Path: filepath.ToSlash(e.Rel), Dir: e.IsDir})
	}
	return res, nil
}
