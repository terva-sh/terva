package modes

// One answer to "put this on the user's clipboard", for every surface
// that offers to.
//
// There are two clipboards in play and they are not the same one. The
// platform tools (pbcopy, wl-copy, xclip) write the clipboard of the
// machine terva runs on. OSC 52 writes the clipboard of the machine the
// TERMINAL runs on, by asking the terminal to do it. Locally these are
// the same machine and the tools are the better option — they are
// verifiable, and nothing can refuse them. Over ssh they are different
// machines and the tools are simply wrong: they copy to a host the user
// will never paste from.
//
// Every caller here runs on the interactive main goroutine (key handling
// and redraw share it), so writing an escape sequence straight to the
// terminal cannot interleave with a frame.

import (
	"errors"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// clipboardRouteFn is a seam: tests pin the route rather than inheriting
// whatever ssh variables the developer's shell happens to carry.
var clipboardRouteFn = tui.DetectClipboardRoute

// copyText puts s on the user's clipboard.
//
// viaTerminal reports that the copy went out as OSC 52, which the caller
// should say out loud: unlike a platform tool, OSC 52 has no reply, so a
// terminal configured to refuse clipboard writes leaves the user with a
// success message and an unchanged clipboard. Telling them which
// mechanism ran is the difference between "it lied" and "oh, that
// setting".
func (i *Interactive) copyText(s string) (viaTerminal bool, err error) {
	if strings.TrimSpace(s) == "" {
		return false, errors.New(i18n.T("nothing to copy"))
	}
	if clipboardRouteFn() == tui.ClipboardTerminal {
		if termErr := i.copyViaTerminal(s); termErr == nil {
			return true, nil
		} else if localErr := writeClipboard(s); localErr != nil {
			// Both routes refused. The terminal's error is the relevant
			// one — on a remote session the local tool was never going
			// to be the right answer even when it works.
			return false, termErr
		}
		return false, nil
	}
	if localErr := writeClipboard(s); localErr == nil {
		return false, nil
	} else if termErr := i.copyViaTerminal(s); termErr == nil {
		return true, nil
	} else {
		// Report the local tool's failure, not the terminal's: "install
		// wl-clipboard" is something the user can act on, where "your
		// terminal ignored a sequence" is not.
		return false, localErr
	}
}

// copyViaTerminal writes the OSC 52 sequence. A nil error means the bytes
// went out, not that the terminal honoured them.
func (i *Interactive) copyViaTerminal(s string) error {
	seq, ok := tui.OSC52Copy(s)
	if !ok {
		return errors.New(i18n.T("too large to send through the terminal"))
	}
	if i.cfg.Terminal == nil {
		return errors.New(i18n.T("no terminal to copy through"))
	}
	_, err := i.cfg.Terminal.Write([]byte(seq))
	return err
}

// copiedNotice is the phrase a surface shows after copyText succeeded.
func copiedNotice(what string, viaTerminal bool) string {
	if viaTerminal {
		return i18n.T("copied %s — via your terminal, which may be set to refuse it", what)
	}
	return i18n.T("copied %s", what)
}

// copyLastReply backs /copy: it puts the model's last reply on the
// clipboard as the model WROTE it — markdown source, no prose gutter, no
// wrap.
//
// That last part is the point. Dragging the same text out of the terminal
// gets you what the screen holds, which is not the same string: every row
// carries terva's two-column indent, and every line long enough to wrap
// carries a newline terva put there. For prose the difference is cosmetic.
// For a block of Python it is the difference between code that runs and
// code that raises IndentationError on the first line.
func (i *Interactive) copyLastReply(arg string) {
	msgs := i.displayTranscript()
	text := ""
	for n := len(msgs) - 1; n >= 0; n-- {
		if msgs[n].Role != provider.RoleAssistant {
			continue
		}
		if t := strings.TrimSpace(assistantText(msgs[n])); t != "" {
			text = t
			break
		}
	}
	if text == "" {
		i.setStatusErr(i18n.T("no reply to copy yet"))
		return
	}

	what := i18n.T("the last reply")
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "reply", "last":
	case "code":
		block, ok := lastFencedBlock(text)
		if !ok {
			i.setStatusErr(i18n.T("the last reply has no code block"))
			return
		}
		text, what = block, i18n.T("the last code block")
	default:
		i.setStatusErr(i18n.T("/copy takes no argument (the last reply), or 'code' for its last code block"))
		return
	}

	viaTerminal, err := i.copyText(text)
	if err != nil {
		i.setStatusErr(i18n.T("copy failed: %s", tui.SanitizeLabel(err.Error())))
		return
	}
	i.setStatusOK(copiedNotice(what, viaTerminal))
}

// lastFencedBlock returns the contents of the last ``` fence in md,
// stripped of the indentation the fence itself sat at.
//
// An UNTERMINATED trailing fence counts. That is not a tolerance for
// malformed markdown — it is the shape of a reply the user cancelled or
// that hit a token limit partway through the code they wanted, which is
// exactly when reaching for /copy code is most likely.
func lastFencedBlock(md string) (string, bool) {
	var found, cur []string
	indent := ""
	open := false
	for _, l := range strings.Split(md, "\n") {
		trimmed := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(trimmed, "```") {
			if open {
				found, cur, open = cur, nil, false
			} else {
				indent, cur, open = l[:len(l)-len(trimmed)], nil, true
			}
			continue
		}
		if open {
			cur = append(cur, strings.TrimPrefix(l, indent))
		}
	}
	if open && len(cur) > 0 {
		found = cur
	}
	if len(found) == 0 {
		return "", false
	}
	return strings.Join(found, "\n"), true
}
