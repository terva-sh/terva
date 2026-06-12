# hello — example terva extension (Go)

A minimal example showing how to register slash commands with the
`ext` SDK.

## Build

```bash
cd examples/extensions/hello
go build -o hello .
```

## Install

```bash
# from this directory
terva ext install .
```

This copies the directory (manifest + binary) into
`$TERVA_HOME/extensions/hello/`. terva picks it up the next time you
launch the TUI.

## Use

In terva, type:

- `/hello`           — agent greets you
- `/hello <name>`    — agent greets `<name>`
- `/summon`          — extension pushes a one-shot notice to the chat

## See also

- `packages/agent/ext` — Go SDK
- `docs/extensions.md` — protocol reference for any-language extensions
