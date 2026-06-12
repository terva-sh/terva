package swarm

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// maxUnixSocketPath is the conservative platform-portable path limit
// for unix sockets. macOS allows 104, linux 108 (including the NUL
// terminator). We pick 100 so the path itself plus a small filename
// tail stays under both caps with a safety margin.
const maxUnixSocketPath = 100

// inboxSocketPath returns a per-agent unix-socket path that's short
// enough to actually work (see maxUnixSocketPath) and unique per
// swarm root so two terva instances on the same machine don't collide.
//
// Strategy:
//
//  1. Try <root>/agents/<id>/in.sock. This is the obvious place and
//     puts everything next to the durable state; on most setups it
//     fits.
//  2. If that's too long, fall back to <tmp>/terva-swarm-<roothash>/<id>.sock.
//     We hash root rather than embedding it so the tmp directory name
//     stays short. SHA-1's first 8 hex chars is plenty: collisions
//     only matter within a single user's tmp dir and we already
//     create a dedicated subdir.
//  3. If even /tmp is somehow too long (chroots, containers), give
//     up with a clear error so the caller surfaces it instead of
//     leaving the user wondering why follow-ups don't work.
func inboxSocketPath(root, agentID string) (string, error) {
	primary := filepath.Join(root, "agents", agentID, "in.sock")
	if len(primary) <= maxUnixSocketPath {
		return primary, nil
	}
	tmp := os.TempDir()
	dir := filepath.Join(tmp, "terva-swarm-"+rootTag(root))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("socket tmp dir: %w", err)
	}
	candidate := filepath.Join(dir, agentID+".sock")
	if len(candidate) <= maxUnixSocketPath {
		return candidate, nil
	}
	// Last-resort: use just the short hash of the id so even very long
	// task slugs fit. We surface the original id in the meta.json /
	// events log; the socket path is purely transport.
	short := shortHash(agentID)
	candidate = filepath.Join(dir, short+".sock")
	if len(candidate) <= maxUnixSocketPath {
		return candidate, nil
	}
	return "", fmt.Errorf("unix socket path too long even after shortening (%s, %d > %d, GOOS=%s)",
		candidate, len(candidate), maxUnixSocketPath, runtime.GOOS)
}

// rootTag returns a stable 8-hex-char tag for the swarm root. Used
// in the tmp-dir name so two parallel terva instances with different
// roots don't share sockets.
func rootTag(root string) string { return shortHash(root) }

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:4])
}
