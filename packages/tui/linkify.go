package tui

// Turning bare URLs in rendered prose into OSC 8 hyperlinks.
//
// The markdown renderer has never understood links (see markdown.go's
// "Not supported" note), so a URL in a model's answer is just text that
// happens to look like one. Terminals paper over this by pattern-matching
// the screen themselves, which works right up until terva's own wrap puts
// a newline in the middle of the URL -- after which the terminal sees two
// short strings and offers to open neither.
//
// Linkifying happens BEFORE the wrap, on the logical line, so a link that
// crosses a row boundary is still one link: wrapANSILineKeepStyle carries
// the open sequence onto the continuation row exactly as it already
// carries open SGR state.

import "strings"

// urlSchemes are the prefixes LinkifyURLs will turn into a link. Kept
// short on purpose: every scheme here is one a terminal will hand to the
// user's browser on a click, and adding file:// or similar would make a
// model's prose able to aim a click at the local filesystem.
var urlSchemes = []string{"https://", "http://"}

// LinkifyURLs wraps bare http(s) URLs in s with OSC 8 hyperlinks, leaving
// everything else -- including any escape sequences already present, and
// any text already inside a hyperlink -- untouched. Returns s unchanged
// when hyperlinks are disabled, which is the only state rendering tests
// see unless they opt in.
func LinkifyURLs(s string) string {
	if !hyperlinksOn.Load() || !strings.Contains(s, "://") {
		return s
	}

	var b strings.Builder
	// prev is the last visible byte emitted, used for the boundary test;
	// 0 means "start of line", which is a boundary. inLink suppresses
	// linkifying the visible text of a link that is already there, so
	// running this twice over the same string is a no-op.
	var prev byte
	inLink := false
	occurrence := 0
	wrote := false

	for i := 0; i < len(s); {
		if n := escSeqLen(s, i); n > 0 {
			seq := s[i : i+n]
			if strings.HasPrefix(seq, "\x1b]8;") {
				inLink = !isHyperlinkClose(seq)
			}
			b.WriteString(seq)
			i += n
			continue
		}
		if !inLink && isURLBoundary(prev) {
			if end := urlEnd(s, i); end > i {
				url := s[i:end]
				b.WriteString(HyperlinkID(url, hyperlinkIDFor(url, occurrence), url))
				occurrence++
				wrote = true
				prev = s[end-1]
				i = end
				continue
			}
		}
		prev = s[i]
		b.WriteString(s[i : i+1])
		i++
	}
	if !wrote {
		return s
	}
	return b.String()
}

// isURLBoundary reports whether a URL may start after the byte c. Anchors
// the match so "notreallyhttps://x" and the tail of an already-consumed
// URL don't produce a second link.
func isURLBoundary(c byte) bool {
	switch c {
	case 0, ' ', '\t', '(', '[', '{', '<', '"', '\'', '`', '*', '_', ',', ';', ':':
		return true
	}
	return false
}

// urlEnd returns the exclusive end offset of the URL starting at s[i], or
// i when no URL starts there.
func urlEnd(s string, i int) int {
	scheme := ""
	for _, sc := range urlSchemes {
		if strings.HasPrefix(s[i:], sc) {
			scheme = sc
			break
		}
	}
	if scheme == "" {
		return i
	}
	end := i + len(scheme)
	for end < len(s) && isURLByte(s[end]) {
		end++
	}
	if end == i+len(scheme) {
		return i // a bare scheme with no host is not a link
	}
	// Everything after the scheme can be trailing punctuation ("https://."
	// at the end of a sentence). What is left is a bare scheme, which is
	// not a link either.
	n := trimURLTail(s[i+len(scheme) : end])
	if n == 0 {
		return i
	}
	return i + len(scheme) + n
}

// isURLByte reports whether c can appear inside a URL as terva scans one
// out of prose. Deliberately permissive about characters that are legal
// in a query string and strict about the ones that end a sentence or an
// escape -- trimURLTail sorts out the ambiguous trailing cases.
func isURLByte(c byte) bool {
	if c <= 0x20 || c >= 0x7f {
		return false
	}
	switch c {
	case '"', '<', '>', '`', '|', '\\', '^':
		return false
	}
	return true
}

// trimURLTail returns how much of body actually belongs to the URL,
// dropping the punctuation that far more often ends the sentence than
// the link. Brackets are dropped only when unbalanced, so a Wikipedia
// URL ending in ")" survives while "(see https://x.example)" does not
// swallow the closing paren.
func trimURLTail(body string) int {
	n := len(body)
	for n > 0 {
		c := body[n-1]
		switch c {
		case '.', ',', ';', ':', '!', '?', '\'', '*', '_':
			n--
			continue
		case ')', ']', '}':
			open := byte('(')
			switch c {
			case ']':
				open = '['
			case '}':
				open = '{'
			}
			if strings.Count(body[:n], string(open)) >= strings.Count(body[:n], string(c)) {
				return n
			}
			n--
			continue
		}
		return n
	}
	return n
}
