# Changelog

Every published version of terva, newest first.

Generated from the release history — the same commits behind the
release-page notes and the in-app changelog — so this file is not
edited by hand. Each heading links to its release page; a version
without one was tagged and installable but never given a page, and
its changes are listed here and nowhere else.

Versions before v0.104.0 predate this release flow and are not
itemised; v0.104.0 is the first curated release.

## [v0.126.13](https://github.com/terva-sh/terva/releases/tag/v0.126.13) — 2026-07-26

### Features

- provider: optionally keep the model's reasoning in the session record

### Fixes

- core: a stuck loop that crosses a turn boundary keeps its place
- tui: keep settings rows inside the dialog frame

## [v0.126.12](https://github.com/terva-sh/terva/releases/tag/v0.126.12) — 2026-07-25

### Features

- personas: a charter can build on the default one instead of forking it
- personas: the roster files onto shelves, and characters group in bulk
- stage: editing a message is a button, not a tap on the message
- stage: the suggest sheet quotes the line you are answering

### Fixes

- stage: a clean bill of health is not the end of the consultation
- web: the session group menu stays inside the board

## [v0.126.11](https://github.com/terva-sh/terva/releases/tag/v0.126.11) — 2026-07-24

### Features

- cards: a character keeps its earlier versions
- web: the control panel's right rail opens without a chat

### Fixes

- stage: wide content scrolls inside a message instead of out of it
- web: the installed app keeps character portraits

## [v0.126.10](https://github.com/terva-sh/terva/releases/tag/v0.126.10) — 2026-07-24

### Features

- sessions: archive a session instead of only deleting it
- stage: a character studio — one screen that owns making and editing
- stage: tell the card doctor what you want, including what to cut
- stage: the other half of the studio — who you play as

### Fixes

- providers: a named local endpoint works the moment you create it
- stage: a save from the card editor no longer closes it mid-consultation

## [v0.126.9](https://github.com/terva-sh/terva/releases/tag/v0.126.9) — 2026-07-23

### Features

- core: activate lazy tool groups on the next model step, not a natural stop
- persona: Mieli orients before multi-step work; drop the contradicting "Act first"
- privfs: create secret-bearing config dirs owner-only; doctor diagnoses a permissive root
- tools: sharpen the model/tool boundary for task state and session_inspect
- web: a way back to the landing once a session is open
- web: first-class planned-restart lifecycle

### Fixes

- ext: reap extension processes promptly instead of leaking zombies

## [v0.126.8](https://github.com/terva-sh/terva/releases/tag/v0.126.8) — 2026-07-23

### Features

- ext: a launcher can say it is still building, and a failure stays visible
- ext: a load-bearing tool stays advertised under lazy visibility
- ext: extensions load in the background, not before the session
- ext: the extension handshake deadline is configurable, and longer
- provider: kimi reports its subscription windows
- web: a session-focused landing for the control panel

### Fixes

- swarm: stream a sub-agent's transcript as it works
- tools: say what actually went wrong
- web: the panel no longer converges concurrent clients on one session

## [v0.126.7](https://github.com/terva-sh/terva/releases/tag/v0.126.7) — 2026-07-22

### Features

- provider: a per-model default reasoning level, and Kimi K3
- stage: a per-card default model
- stage: pick the model and open the character's card from the session header

### Fixes

- stage: keep the phone chat header readable
- stage: recover the safe-area insets on a cold PWA launch and behind Safari's toolbar
- web: show a newly logged-in provider's models without a refresh

## [v0.126.6](https://github.com/terva-sh/terva/releases/tag/v0.126.6) — 2026-07-22

### Features

- card: lint example dialogue missing <START>, and teach the doctor to fix it
- stage: sort the card library, and favorite cards to the top
- web: every button shows a press — a shared :active feedback

### Fixes

- stage: the edit-message action row wraps, and reads as one primary
- stage: the ⓘ hints work on touch, and the sort control gets room
- web: freeze the notch insets so a top bar survives a fixed overlay

## [v0.126.5](https://github.com/terva-sh/terva/releases/tag/v0.126.5) — 2026-07-22

### Features

- stage: filter the library by group — show-only, hide, and derived origins
- web: filter the session board by group, hiding Stage play by default

### Fixes

- stage: the "+ New" buttons follow the page style, not the OS default
- stage: the card and persona detail sheets get room on a wide screen

## [v0.126.4](https://github.com/terva-sh/terva/releases/tag/v0.126.4) — 2026-07-21

### Features

- stage: group cards into browsable buckets, apart from tags
- stage: group chats into buckets too, with an add-chats sheet
- web: session groups on the control panel — filter and file from the board

## [v0.126.3](https://github.com/terva-sh/terva/releases/tag/v0.126.3) — 2026-07-21

### Features

- stage: a default model per character, scoped to a World
- stage: every agentic surface names the model it will use, and lets you change it
- stage: the cartographer's door, world tools, and a jump-to-latest button
- stage: turn a cartographer conversation into a playable world

### Fixes

- stage: the phone composer wraps to two rows on a narrow screen

## [v0.126.2](https://github.com/terva-sh/terva/releases/tag/v0.126.2) — 2026-07-20

### Fixes

- stage: keep the top bar out from under the iPhone notch

### Other

- web: a shared design base — a 16px input floor, resets, and an enforced --ui-* token contract

## [v0.126.1](https://github.com/terva-sh/terva/releases/tag/v0.126.1) — 2026-07-20

### Features

- i18n: Stage joins localization, and the operator prose crosses the wire
- personas: Kartoittaja — the creator persona a seed pass chose
- stage: export a session as a story
- stage: the session doctor and the tools a played scene earns

### Fixes

- ctrlproto: a verb must be dispatched on an arm of its own
- permissions: the durable tool-allow grant, wired and deduplicated
- stage,web: the live bugs the shared layers were hiding

### Other

- web: a transport seam for the panel, and one fold for a session's events

## [v0.126.0](https://github.com/terva-sh/terva/releases/tag/v0.126.0) — 2026-07-19

### Features

- card: read CCv3 cards, survive oversized imports, and lint on the card sheet
- stage: Stage is an installable, offline-capable PWA at /stage/
- stage: Worlds — an ensemble stage with a roster, scoped lore, and a meta-narrator
- stage: directed authorship — draft and post a line as anyone, or direct the story
- stage: library upkeep — a persona editor, deletes that clean up, and titles that improve themselves
- stage: model choice per session and per generation, with provider and billing on every row
- stage: the immersive craft guards — six style levers for long scenes
- stage: turns you can trust — reconnects resubscribe, approvals render, and Stop stops
- stage: who you are reaches every prompt — gender and pronouns, pinned tense, side chats included
- CHANGELOG.md — the release history, rendered from the same commits that make the notes

### Fixes

- session: timestamp the settings timeline, and rescue year-one greetings at load
- workspace: every side-channel model call books its spend

## [v0.125.1](https://github.com/terva-sh/terva/releases/tag/v0.125.1) — 2026-07-19

### Features

- mcp: Streamable HTTP transport for remote MCP servers
- Stage — an immersive chat and play surface for character cards
- external agent workers — drive foreign coding agents as swarm workers
- git worktree management, in the TUI and on the web
- native image output — generated images inline from Codex
- the orchestration board — a live sessions board with a swarm lane
- the workflow engine — scriptable, multi-agent workflows

## [v0.124.2](https://github.com/terva-sh/terva/releases/tag/v0.124.2) — 2026-07-15

### Features

- read the attach bearer token from --token-file
- ship deploy examples in the binary

## [v0.124.1](https://github.com/terva-sh/terva/releases/tag/v0.124.1) — 2026-07-15

### Features

- --task to preload the prompt from a file
- reload terva in place on SIGHUP (systemctl reload)

### Fixes

- tui: repaint on a same-size SIGWINCH so a mux reattach isn't blank

## [v0.124.0](https://github.com/terva-sh/terva/releases/tag/v0.124.0) — 2026-07-15

### Features

- core: raise the compaction ledger cap so a long session's actions survive
- escalate a stuck agent to a stronger model, and show it happening

## [v0.123.3](https://github.com/terva-sh/terva/releases/tag/v0.123.3) — 2026-07-14

### Fixes

- provider: retry transient errors that arrive inside a stream
- web: show a named OpenAI-compatible endpoint's models in the picker

## [v0.123.2](https://github.com/terva-sh/terva/releases/tag/v0.123.2) — 2026-07-14

### Fixes

- web: a service worker cannot install what it is not allowed to fetch

## [v0.123.1](https://github.com/terva-sh/terva/releases/tag/v0.123.1) — 2026-07-14

### Features

- core: compaction stops paying for a cold read

### Fixes

- web: let the daemon answer a navigation, and keep the socket alive

## [v0.123.0](https://github.com/terva-sh/terva/releases/tag/v0.123.0) — 2026-07-14

### Features

- auth,tui,web: name an endpoint, and run several OpenAI-compatible servers
- provider: register claude-sonnet-5
- web: edit a model's settings from the picker

### Fixes

- core: a resumed session names the build that is actually writing it
- core,provider: keep max_tokens inside the model's output cap on a swap
- tui,web: stream text at an even rate, whatever the provider does

## [v0.122.0](https://github.com/terva-sh/terva/releases/tag/v0.122.0) — 2026-07-14

### Features

- auth: sign in to a provider without leaving terva
- cli: terva doctor — what terva can actually do here
- tools: let `write` set an explicit file mode
- transcript: compaction stops deleting your scrollback
- transcript: the panel stops downloading your whole conversation every turn

### Fixes

- web: stay signed in, and keep the token off the process table

## [v0.121.4](https://github.com/terva-sh/terva/releases/tag/v0.121.4) — 2026-07-13

### Features

- web,tui: set a model as the default, from the panel or over the wire

### Fixes

- codex: cache the reset-credit list, and drop it the moment one is spent
- codex: drop the phantom usage window, and name the durations OpenAI sends
- queue: announce the queue the agent loop drains out from under the host
- web: the mobile composer no longer zooms the page off its own send button

## [v0.121.3](https://github.com/terva-sh/terva/releases/tag/v0.121.3) — 2026-07-13

### Features

- jail: unjail a directory for good, the way trust already works
- web: sign in to the panel without putting the token in a URL

### Fixes

- docs: install every doc, not a hand-maintained subset
- provider: real context windows for models discovered at runtime
- web: explain the no-auth Host rejection instead of just refusing
- workspace: a tool rebuild no longer severs the ask channel

## [v0.121.1](https://github.com/terva-sh/terva/releases/tag/v0.121.1) — 2026-07-13

### Fixes

- a unix socket path that is not a socket is refused, not deleted
- an attached terminal no longer crashes on a session switch
- api-key login works on a headless host
- self-restart is offered only where it works, and refusing one no longer costs a turn

## [v0.121.0](https://github.com/terva-sh/terva/releases/tag/v0.121.0) — 2026-07-12

### Features

- @-file completion in both front ends, over the wire
- lazy tool groups — advertise less, activate on demand
- native task boards, session_inspect, and prompt-size attribution
- one settings surface, rendered by both front ends
- self-restart and an operator's view of the running build
- sessions you can find again — resume picker and generated titles
- terva attach — the TUI as a client of a running daemon

### Fixes

- isolation, lifecycle, and layout correctness

## [v0.120.1](https://github.com/terva-sh/terva/releases/tag/v0.120.1) — 2026-07-11

### Fixes

- test: make the usage-throttle refresh test robust on coarse (Windows) clocks

## [v0.120.0](https://github.com/terva-sh/terva/tree/v0.120.0) — 2026-07-11

Tagged and installable, but never given a release page; these
changes are itemised nowhere else.

### Features

- ext: terva ext doctor — diagnose extensions from the command line
- models: GPT-5.6 cache-write pricing and a model-max / working-window split
- provider: a native max thinking tier above maximum
- tui: grouped tool-display mode, a shared tool-run summary, and a "max" reasoning level
- tui,ext: cheap wins — wider tool-arg display, an extension submit frame, a live bash body
- usage: consumable rate-limit resets — list and redeem banked codex resets

### Fixes

- build: pin embedded i18n catalogs and builtin personas to LF for reproducible cross-platform builds
- raati: never overwrite a record written in the same clock tick
- reliability: a terminal RPC compact contract, headless-refusal coverage, and MCP tool withdrawal on server death
- security: private-state file modes, bounded HTTP response bodies, and extension-name validation
- usage: mid-turn meter refresh, bare-id provider pin, and snapshot carry-over across client rebuilds
- port upstream correctness fixes — image MIME sniffing, post-compaction fork, stdio MCP cwd, codex error payloads, host-routed tool refresh

### Other

- agent: sever the workspace->modes and build->tui layering and drop dead lint
- web: modularize the control-panel client into platform / features / ui, and regenerate the bundle

## [v0.119.3](https://github.com/terva-sh/terva/releases/tag/v0.119.3) — 2026-07-10

### Fixes

- raati: never overwrite a record written in the same clock tick

## [v0.119.2](https://github.com/terva-sh/terva/tree/v0.119.2) — 2026-07-10

Tagged and installable, but never given a release page; these
changes are itemised nowhere else.

### Fixes

- ci: pin the embedded builtin personas to LF

## [v0.119.1](https://github.com/terva-sh/terva/tree/v0.119.1) — 2026-07-10

Tagged and installable, but never given a release page; these
changes are itemised nowhere else.

### Fixes

- ci: pin the i18n reference catalogs to LF

## [v0.119.0](https://github.com/terva-sh/terva/tree/v0.119.0) — 2026-07-10

Tagged and installable, but never given a release page; these
changes are itemised nowhere else.

### Features

- core: context pressure managed across the whole turn lifecycle
- provider: GPT-5.6 tiers, per-request credentials, pinned prompt caching
- raati: deliberation councils — convene a panel, get a decision
- tui: the interactive TUI runs entirely on the daemon carrier
- workspace: the daemon owns the chat bridge

### Fixes

- provider: bounded streams, no stranded readers, ephemeral context on Responses
- swarm: scoped lifetimes, trust-gated spawn, truthful recaps
- tui: chat and sidechat input can never reach the host shell
- web: hardening bundle — MIME allowlist, CSP, escaping, no hub stalls
- wire: one bounded, recoverable frame reader on every line protocol

### Other

- agent: split the agent host into build, config, and workspace packages

## [v0.118.0](https://github.com/terva-sh/terva/releases/tag/v0.118.0) — 2026-07-06

### Features

- kinded notices with a prompt_rebuilt cache-break notification
- make the control-plane carrier the default interactive TUI backend

### Fixes

- announce terva web startup and print ready only after the socket binds
- close the carrier daemon's live-view gaps (shared with the web client)

## [v0.117.1](https://github.com/terva-sh/terva/releases/tag/v0.117.1) — 2026-07-05

### Fixes

- Windows-tolerant temp-dir cleanup in the session test suite

## [v0.117.0](https://github.com/terva-sh/terva/releases/tag/v0.117.0) — 2026-07-05

### Features

- --web-insecure-cidr for scoped no-auth access over a trusted overlay
- guild-scoped admissions — a kick revokes the whole guild
- image generation via a generate_image tool
- split the interactive TUI into its own translation catalog

### Fixes

- keep the interactive chat bridge DM-only
- order concurrent replay emits and keep the scrubber live
- re-subscribe on reconnect and carry the web token in the browser

## [v0.116.0](https://github.com/terva-sh/terva/releases/tag/v0.116.0) — 2026-07-05

### Features

- drive the interactive TUI through the control plane
- images on the control plane
- terva replay — play back a recorded session in the TUI

### Fixes

- rebind the status connector after a cross-provider model or credential swap

## [v0.115.1](https://github.com/terva-sh/terva/releases/tag/v0.115.1) — 2026-07-04

### Fixes

- deps: bump golang.org/x/image to v0.41.0

## [v0.115.0](https://github.com/terva-sh/terva/releases/tag/v0.115.0) — 2026-07-04

### Features

- internationalization — a translation foundation and fully localized UI
- split auto-swarm into tool-availability and proactive-nudge toggles
- terva web — a browser control panel for a self-hosted terva

## [v0.114.2](https://github.com/terva-sh/terva/releases/tag/v0.114.2) — 2026-07-02

### Fixes

- deps: bump golang.org/x/image to v0.38.0

## [v0.114.1](https://github.com/terva-sh/terva/releases/tag/v0.114.1) — 2026-07-02

### Fixes

- connlocal: tests hold the engine log open past cleanup on Windows

## [v0.114.0](https://github.com/terva-sh/terva/releases/tag/v0.114.0) — 2026-07-02

### Features

- bot: group admission, per-chat sessions, approvals over chat, typed chat events
- cli: capability scoping and model discovery — --extensions, --mcp, filterable --list-models
- connectors: connector protocol 2 — identity, capabilities, asks, speakers, threads, chat events
- discord: built-in Discord connector — buttons, speakers, threads, zero privileged intents
- ext: connector extensions — one process, both roles (experimental) + service-aware /connect
- release: multi-arch container image — ghcr.io/terva-sh/terva

### Fixes

- bot,core: durable bot transcripts, session identity in terva_status, per-conversation tool state, clean shutdown
- cards: pin JSON card fixtures to LF so Windows checkouts match their PNG twins
- provider: constant format strings for the go 1.24 vet printf check

## [v0.113.1](https://github.com/terva-sh/terva/releases/tag/v0.113.1) — 2026-07-02

### Fixes

- card fixtures parse identically on Windows checkouts

## [v0.113.0](https://github.com/terva-sh/terva/releases/tag/v0.113.0) — 2026-07-02

### Features

- character cards (CCv2) for chat and play
- lore — authored, keyword-triggered context
- play gains a cast — declared characters voiced by live actors
- prompt manifest and --dump-prompt
- status line v2 — segments, meters, git, scripts, daltonized themes
- tool display modes, grouped command menu, live tool progress

### Fixes

- streamed tool output could corrupt the TUI frame

## [v0.112.0](https://github.com/terva-sh/terva/releases/tag/v0.112.0) — 2026-06-30

### Features

- --chat, --play, and --no-workspace-tools modes
- bots are full agents — extensions, MCP, and a proactive idle nudge
- ext.Sequential() — keep order-sensitive extension tools in order
- immersive personas — a charter can own the agent's identity
- project-scoped agents — a self-contained agent per directory

## [v0.111.0](https://github.com/terva-sh/terva/releases/tag/v0.111.0) — 2026-06-30

### Features

- built-in profiling and a faster streaming render loop
- personas — a swappable agent identity with a specialist crew

## [v0.110.0](https://github.com/terva-sh/terva/releases/tag/v0.110.0) — 2026-06-25

### Features

- /usage — subscription credits, rate-limit windows, and a status-bar hint
- extensions — withdraw tools and swap context per session (protocol 4)
- skills — author, run, and live-reload from the TUI

### Fixes

- keep the prompt prefix stable for the whole turn
- read's paging hints and past-EOF errors speak lines, not bytes
- resolve the extension config dialog by manifest name

## [v0.109.1](https://github.com/terva-sh/terva/releases/tag/v0.109.1) — 2026-06-22

### Features

- canonical extension install names and pack-install migration
- extensions can publish configuration templates
- inspect why an extension or MCP server is off
- integrate the new management dialogs into the interactive shell
- manage MCP servers from the interactive TUI with /mcp
- paste images from the system clipboard

### Fixes

- DeepSeek V4 is text-only at the wire
- swarm sub-agents inherit and pin the host provider/model

## [v0.109.0](https://github.com/terva-sh/terva/releases/tag/v0.109.0) — 2026-06-21

### Features

- agent: recover from a provider rejecting an image, persisted
- agent: tool-call audit log + bash ergonomics
- model: two-level /model picker with cross-provider favorites
- provider: discover gateway models live + version the model cache
- provider: named OpenAI-compatible endpoints + a migration helper
- swarm: configurable per-provider weak/medium/strong tiers

### Fixes

- tui: live-agent /new & /sessions, a session-open crash, panel wrapping

## [v0.108.4](https://github.com/terva-sh/terva/releases/tag/v0.108.4) — 2026-06-21

### Features

- model: per-model temperature + a registry for scalar model params
- model: session-only /model switches + Ctrl+D promote to project/global default

### Fixes

- ext: show --ext session loads in /extensions
- tui: /context modal — wrap text, surface ext guidance, mark compaction
- tui: /new and /clear wipe the screen + scrollback
- tui: colour-preserving line wrap (WrapANSILineKeepStyle)

## [v0.108.3](https://github.com/terva-sh/terva/releases/tag/v0.108.3) — 2026-06-20

### Features

- ext: targeted `terva ext upgrade <name>...`
- provider: scoped --insecure TLS for a self-signed --base-url
- tools: dedup re-reads of an unchanged file still in context
- tui: /context becomes a tabbed modal — size breakdown + extension text

### Fixes

- core: treat "nothing to compact" as a benign no-op, not a failure
- ext: resolve ext subcommands by manifest name, not just the dir
- extensions: isolate subprocess process groups and reap them on teardown
- jail,tui: emit OSC 7 cwd, allow cd into subdirs, relative DisplayPath
- update: compare only the x.y.z core so dev builds aren't offered an older release

## [v0.108.2](https://github.com/terva-sh/terva/releases/tag/v0.108.2) — 2026-06-20

### Features

- agent: sampling temperature control (--temperature / config)
- tui: Shift+Enter newline, plus band-jump and resume-scroll fixes

### Fixes

- chat: correct telegram bot process checks on Windows

## [v0.108.1](https://github.com/terva-sh/terva/releases/tag/v0.108.1) — 2026-06-20

### Features

- extensions: bulk-install packs + first-run core pack
- extensions: observable event & hook surface
- jail: scoped read-only sandbox roots + local-data authority
- tui: manage extensions and edit model config in-session

### Fixes

- provider: GLM /v4 base path and Bedrock image handling

## [v0.108.0](https://github.com/terva-sh/terva/releases/tag/v0.108.0) — 2026-06-19

### Features

- agent: AST-based bash permission scopes
- agent: per-subagent model tier for sub-agent spawns
- ext: author-side SDK for protocol v3
- ext: extension protocol v3 — host tool calls, session reads, refreshable context

## [v0.107.0](https://github.com/terva-sh/terva/releases/tag/v0.107.0) — 2026-06-19

### Features

- SSRF/egress guard for outbound network safety
- expand the agent tool surface and permission model

### Other

- extract the extension wire into a dependency-light driver

## [v0.106.2](https://github.com/terva-sh/terva/releases/tag/v0.106.2) — 2026-06-15

### Fixes

- close MCP server log file handles on shutdown

## [v0.106.1](https://github.com/terva-sh/terva/releases/tag/v0.106.1) — 2026-06-15

### Features

- inspect local images with read — discoverable and vision-aware
- per-agent swarm worktree isolation + an extension SubmitSlash hook
- template-driven greeting and spinner flavor
- workspace trust — gate auto-acting project content behind explicit trust

## [v0.106.0](https://github.com/terva-sh/terva/releases/tag/v0.106.0) — 2026-06-14

### Features

- Agent Client Protocol (ACP) editor run mode
- ship a full and a lean release binary

### Fixes

- cross-platform path handling and Windows test cleanup
- mirror tool-output images into the conversation only when present

## [v0.105.3](https://github.com/terva-sh/terva/releases/tag/v0.105.3) — 2026-06-13

### Fixes

- make the extension data-layer guard and project key cross-platform

## [v0.105.2](https://github.com/terva-sh/terva/releases/tag/v0.105.2) — 2026-06-13

### Features

- extensions: protocol v2 — per-project data dirs, session cwd, read-only tools, config-disable

### Fixes

- core,sdk: model-swap safety, standard turn policy, reasoning wire, step cap

## [v0.105.1](https://github.com/terva-sh/terva/releases/tag/v0.105.1) — 2026-06-13

### Features

- extensions: context contributions — extensions can shape model context

### Fixes

- extensions: protocol robustness — oversized frames and headless session id

## [v0.105.0](https://github.com/terva-sh/terva/releases/tag/v0.105.0) — 2026-06-13

### Features

- core: a shared turn policy and a resilient swarm protocol
- extensibility: MCP tools, tool-use hooks, and extension protocol v2
- permissions: approval modes, a workspace default, and a jailed sandbox
- tools: edit gains replaceAll and whitespace-tolerant matching
- tui: grapheme-aware editing and bracketed-paste hardening

### Other

- decompose the interactive loop, unify registries, sync docs

## [v0.104.3](https://github.com/terva-sh/terva/releases/tag/v0.104.3) — 2026-06-11

### Fixes

- tests: two more platform bugs in the migration tests

## [v0.104.2](https://github.com/terva-sh/terva/releases/tag/v0.104.2) — 2026-06-11

### Fixes

- tests: first contact with windows CI — three platform bugs
- custom-endpoint models get a real context window
- go-install builds report their real version
- the install one-liners live at terva.sh
- zot migration skips dead sockets instead of failing

## [v0.104.0](https://github.com/terva-sh/terva/releases/tag/v0.104.0) — 2026-06-11

### Features

- terva — the renamed continuation of zot
- a layered model catalog with live discovery and capability tags
- agent and TUI growth across the fork era
- external chat connectors — bridge terva to any chat service
- session format v2 and a hardened agent loop
- ship from GitHub — release workflow, goreleaser config, installers
- terva migrate — the opt-in move off zot's locations
