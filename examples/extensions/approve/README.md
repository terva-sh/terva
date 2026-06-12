# approve — example terva extension

Demonstrates the **spontaneous `open_panel`** pattern: an
extension-registered tool opens a panel from inside its handler
goroutine, blocks until the user responds, then returns the result to
the model.

## What it does

Registers one LLM-callable tool:

```
approve_action(action, reason?)
```

When the model calls it, a panel appears in the TUI:

```
╭─ Approval required ─────────────────────╮
│  Action:  delete /tmp/build              │
│  Reason:  cleaning up after the build    │
│                                          │
│    y  approve      n / esc  deny         │
╰──────────────────────────────────────────╯
  y approve  n deny  esc cancel
```

The model's tool call is held open until the user presses a key. The
model receives `"approved"` or `"denied: user rejected the action"` as
the tool result and replies accordingly.

## Build

```bash
cd examples/extensions/approve
go build -o approve .
```

## Install

```bash
terva ext install .
```

## Try it

In terva, ask:

> Request approval to delete the temp directory.

The model calls `approve_action`; the panel opens. Press **y** to
approve, **n** or **esc** to deny.

> Ask me to approve before running any destructive command.

The model will start calling `approve_action` before suggesting risky
steps; each call pauses until you respond.

## Key points

- The tool handler calls `e.OpenPanel(...)` directly — no slash command
  needed.
- A `chan bool` bridges the panel key handler back to the blocked tool
  goroutine; nothing in the wire protocol changes.
- Panel IDs are unique per call so multiple concurrent approvals don't
  collide (rare in practice, but handled correctly).
- The `onClose` callback on `OnPanelKey` handles the case where the
  user dismisses the panel from the TUI border rather than pressing a
  key, treating it as a denial.

## See also

- `examples/extensions/secret` — same pattern with masked text input
  (credential collection)
- `examples/extensions/guard` — blocks dangerous tool calls via
  interception (no panel)
- `docs/extensions.md` — full protocol reference
