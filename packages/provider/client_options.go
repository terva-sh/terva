package provider

// ClientOption hands a client something from its host that the wire must not
// do itself: write a file, run a program, read the environment. Decision 0021,
// rule 5: the wire does no I/O.
//
// Every Anthropic-wire and Gemini-wire constructor takes the same options, and
// a wire ignores one it has no use for, so a host can pass one set to every
// constructor. A client given none does no I/O and loses only what the option
// would have supplied.
type ClientOption func(*clientHooks)

// clientHooks is what the options set. The zero value does nothing.
type clientHooks struct {
	dump          func(body []byte)
	claudeVersion func() string
	saveImage     ImageSaver
}

func applyClientOptions(opts []ClientOption) clientHooks {
	var h clientHooks
	for _, o := range opts {
		if o != nil {
			o(&h)
		}
	}
	return h
}

// WithRequestDump gives the Anthropic wire a sink for each outgoing request
// body, called before the request is sent. terva's harness appends each one to
// the file named by TERVA_DEBUG_ANTHROPIC, to diff one turn's request against
// the next when a cache prefix stops matching.
func WithRequestDump(dump func(body []byte)) ClientOption {
	return func(h *clientHooks) { h.dump = dump }
}

// WithClaudeCodeVersion tells the Anthropic wire which Claude Code version is
// installed on this machine, for the user-agent its OAuth requests present. It
// is called on every such request, so it must not block: return "" until the
// version is known. The wire claims the newer of it and the compiled baseline.
func WithClaudeCodeVersion(installed func() string) ClientOption {
	return func(h *clientHooks) { h.claudeVersion = installed }
}

// ImageSaver stores an image a model generated and returns the path it was
// saved at. Where it saves is the host's to decide; the wire does not know a
// directory. The Gemini wire calls it for each image in a response and tells
// the model the path in a text block beside the image; the image bytes reach
// the transcript either way.
type ImageSaver func(mimeType string, data []byte) (path string, err error)

// WithImageSaver gives the Gemini wire somewhere to save generated images.
// Without it the wire saves nothing and adds no path.
func WithImageSaver(save ImageSaver) ClientOption {
	return func(h *clientHooks) { h.saveImage = save }
}
