package agent

import (
	"context"
	"sort"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extproto"
)

// Extension slash commands, surfaced to the browser as a pane of buttons rather
// than a command line. The TUI dispatches "/name" typed by the operator; the web
// has no such prompt, so each registered command becomes a button in the
// "commands" surface and running one is a surface.action. The command's Response
// (open a panel, submit a prompt, show a note) is applied here — the web analogue
// of the TUI's invokeExtensionCommand and ACP's acpInvokeExtCommand. (User-driven
// slash commands like /skill are a separate, future concern; this is only the
// extension-provided set.)

// webExtCommandTimeout caps how long the daemon waits for an extension to answer
// a command run from the web, matching the ACP host's budget.
const webExtCommandTimeout = 30 * time.Second

// commandsView lists every extension-registered command for the commands pane,
// ordered by extension then name so the client can group them stably.
func (s *wsSession) commandsView() *ctrlproto.CommandsView {
	v := &ctrlproto.CommandsView{}
	if s.extMgr == nil {
		return v
	}
	cmds := s.extMgr.Commands()
	sort.Slice(cmds, func(i, j int) bool {
		if cmds[i].Extension != cmds[j].Extension {
			return cmds[i].Extension < cmds[j].Extension
		}
		return cmds[i].Name < cmds[j].Name
	})
	for _, c := range cmds {
		v.Commands = append(v.Commands, ctrlproto.CommandEntry{Ext: c.Extension, Name: c.Name, Description: c.Description})
	}
	return v
}

// commandAction runs an extension command named by args["name"] (with optional
// args["args"]) and applies its response. Only the "run" action is defined.
func (s *wsSession) commandAction(action string, args map[string]string) error {
	if action != "run" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown commands action %q", action)
	}
	name := args["name"]
	if name == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "commands run: missing name")
	}
	if s.extMgr == nil || !s.extMgr.HasCommand(name) {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "no command /%s", name)
	}
	ctx, cancel := context.WithTimeout(s.ws.ctx, webExtCommandTimeout)
	defer cancel()
	resp, err := s.extMgr.Invoke(ctx, name, args["args"], webExtCommandTimeout)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "command /%s: %v", name, err)
	}
	s.applyCommandResponse(s.extMgr.CommandOwner(name), resp)
	return nil
}

// applyCommandResponse maps an extension command's Response onto the web: a
// panel opens, a prompt runs a turn, and display/insert/error become one-shot
// notices in the conversation area. ext attributes any notice to its extension.
// A noop (or a handler that opened a panel as a side effect, like todo) applies
// nothing here — the panel already surfaced via the OpenPanel host hook.
func (s *wsSession) applyCommandResponse(ext string, resp extproto.CommandResponseFromExt) {
	if resp.Error != "" {
		s.broadcast(ctrlproto.NoticeEvent("error", ext, resp.Error))
		return
	}
	switch resp.Action {
	case "open_panel":
		if resp.OpenPanel != nil {
			s.paneOpen(ext, *resp.OpenPanel)
		}
	case "prompt":
		if resp.Prompt != "" {
			if err := s.prompt(resp.Prompt, nil); err != nil {
				s.broadcast(ctrlproto.NoticeEvent("error", ext, err.Error()))
			}
		}
	case "display":
		s.broadcast(ctrlproto.NoticeEvent("info", ext, resp.Display))
	case "insert":
		// The web has no shared command line to fill (a button, not a slash
		// prompt), so the would-be inserted text is shown as a note instead — the
		// same degrade ACP mode makes. Nothing is lost; it just isn't editable.
		s.broadcast(ctrlproto.NoticeEvent("info", ext, resp.Insert))
	case "noop", "":
		// Nothing to apply.
	}
}
