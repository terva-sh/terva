package build

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"terva.sh/terva/packages/agent/cliversion"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/provider"
)

// hostOptions is what terva's harness does for a client that the wire does not
// do itself (decision 0021, rule 5). The same options go to every Anthropic-wire
// and Gemini-wire constructor, and each wire takes the ones it uses, so a
// registry row cannot lose one by naming the wrong set.
func (c clientConfig) hostOptions() []provider.ClientOption {
	dir := c.CWD
	return []provider.ClientOption{
		provider.WithRequestDump(debugAnthropicDump),
		provider.WithImageSaver(func(mimeType string, data []byte) (string, error) {
			return saveGeneratedImage(dir, mimeType, data)
		}),
		provider.WithClaudeCodeVersion(cliversion.Claude),
	}
}

// debugAnthropicDump appends an outgoing Anthropic request body to the file
// named by TERVA_DEBUG_ANTHROPIC, one JSON object per line, to diff turn N
// against turn N+1 when a cache prefix stops matching. The variable is read on
// every request, as it was when the wire did this itself, and an unset one
// writes nothing.
func debugAnthropicDump(body []byte) {
	path := envcompat.Get("DEBUG_ANTHROPIC")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(body)
	_, _ = f.Write([]byte{'\n'})
	_ = f.Close()
}

// saveGeneratedImage writes an image a model generated into dir, the session's
// workspace (clientConfig.CWD), and returns its path for the model to read.
//
// An empty dir means the process working directory. terva never chdirs, so
// that is the launch directory rather than the session's workspace; only a
// client built without a Resolve (a test, say) has no CWD to give.
func saveGeneratedImage(dir, mimeType string, data []byte) (string, error) {
	ext := ".png"
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	name := "terva-gemini-image-" + uuid.NewString() + ext
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
