// Package dialogs holds the modal overlays the interactive TUI opens:
// the login, model, session, settings, permissions, extension, MCP and
// swarm pickers, plus the confirm/question prompts the agent blocks on.
//
// Each dialog is a self-contained tui component. It owns its own state,
// draws itself through Render, and consumes keys through HandleKey. The
// host in packages/agent/modes constructs one, opens it, forwards keys to
// it, and reads the result back.
//
// The rule for this package: a dialog knows nothing about the loop that
// opens it. There is no *Interactive here, no Carrier, no ExtensionHost,
// and no *core.Agent. Dialogs take plain values -- messages, summaries, a
// small asker interface -- and hand back plain values. That was already true
// before this package existed; the split only made it checkable.
//
// It mattered through plan 4.1: packages/agent/modes reached the live agent
// through the AgentFor crutch (~22 reads, all on *Interactive receivers), and
// keeping this package free of them meant the retirement stayed an in-package
// refactor of the loop. 4.1 is done — the crutch is gone, the TUI holds no
// agent — and this rule is why the last reader (/btw) could move to an asker
// over the sidechat surface without dragging the dialog package across the seam.
package dialogs
