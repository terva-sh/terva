package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/chat/external"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/core"
)

// detachChild configures cmd to run in its own process group so tty
// signals sent to the parent (SIGINT, SIGHUP on logout) don't also
// reach the detached bot. Platform-specific: setsid on unix, a noop
// on windows (Go's spawn path already detaches when no console is
// inherited). See botcmd_unix.go and botcmd_windows.go.
var detachChild func(cmd *exec.Cmd)

// runBotCommand dispatches `terva bot ...` subcommands. The connector
// is chosen with --connector NAME (default: the sole registered
// service). `terva telegram-bot ...` and `terva tg ...` remain as aliases
// that pin --connector=telegram.
func runBotCommand(rawArgs []string, version string) (handled bool, err error) {
	if len(rawArgs) == 0 {
		return false, nil
	}
	connectorName := ""
	switch rawArgs[0] {
	case "bot":
		// connector from --connector or the registry default
	case "telegram-bot", "tg":
		connectorName = "telegram"
	default:
		return false, nil
	}
	sub := ""
	var tail []string
	if len(rawArgs) >= 2 {
		sub = rawArgs[1]
		tail = rawArgs[2:]
	}

	// `terva bot link` installs an external connector manifest; it
	// resolves no service, so handle it before the lookup below.
	if sub == "link" {
		return true, botLink(tail)
	}

	tail, flagged := extractConnectorFlag(tail)
	if flagged != "" {
		connectorName = flagged
	}
	// Dev manifests register before the name is resolved so
	// `terva bot run --connector-manifest ./x.json` needs no
	// --connector. They stay in the tail: `bot start` re-execs
	// `bot run` with the same args, and the child must re-load them.
	devNames, err := registerDevManifests(tail)
	if err != nil {
		return true, err
	}
	if connectorName == "" && len(devNames) == 1 {
		connectorName = devNames[0]
	}
	if connectorName == "" {
		connectorName = chat.DefaultServiceName()
	}
	if connectorName == "" {
		return true, fmt.Errorf("no chat connectors compiled into this binary (built with -tags terva_no_telegram?)")
	}
	svc, ok := chat.Lookup(connectorName)
	if !ok {
		return true, fmt.Errorf("unknown connector %q (available: %s)", connectorName, serviceNames())
	}

	switch sub {
	case "", "help", "-h", "--help":
		printBotHelp()
		return true, nil
	case "setup":
		if svc.Setup == nil {
			return true, fmt.Errorf("connector %q has no setup flow", svc.Name)
		}
		return true, svc.Setup(TervaHome())
	case "status":
		return true, botStatus(svc)
	case "reset":
		return true, botReset(svc)
	case "run":
		return true, botRun(svc, tail, version)
	case "start":
		return true, botStart(svc, tail)
	case "stop":
		return true, botStop(svc)
	case "logs":
		return true, botLogs(svc, tail)
	default:
		printBotHelp()
		return true, fmt.Errorf("unknown bot subcommand %q", sub)
	}
}

// registerDevManifests loads every --connector-manifest PATH found in
// args (left in place — ParseArgs knows the flag and `bot start`
// forwards the tail verbatim to the detached `bot run`). Returns the
// registered service names.
func registerDevManifests(args []string) ([]string, error) {
	var names []string
	for i := 0; i < len(args); i++ {
		path := ""
		if args[i] == "--connector-manifest" && i+1 < len(args) {
			path = args[i+1]
			i++
		} else if v, ok := strings.CutPrefix(args[i], "--connector-manifest="); ok {
			path = v
		}
		if path == "" {
			continue
		}
		name, err := external.RegisterManifest(path)
		if err != nil {
			return nil, fmt.Errorf("--connector-manifest %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "terva: dev connector %q loaded for this run (%s)\n", name, path)
		names = append(names, name)
	}
	return names, nil
}

// botLink implements `terva bot link <connector.json>`: symlink the
// manifest into $TERVA_HOME/connectors/<name>/ so it persists across
// runs as a visible, auditable artifact (`terva bot status` shows the
// link target; `terva bot reset` removes it).
func botLink(tail []string) error {
	if len(tail) != 1 {
		return fmt.Errorf("usage: terva bot link <path/to/connector.json>")
	}
	dst, err := external.Link(TervaHome(), tail[0])
	if err != nil {
		return err
	}
	fmt.Println("linked", dst)
	fmt.Println("the connector now loads in every terva run; `terva bot reset --connector <name>` unlinks it.")
	return nil
}

// extractConnectorFlag strips --connector NAME / --connector=NAME
// from args and returns the remainder plus the chosen name ("" when
// absent). Remaining args pass through to ParseArgs untouched.
func extractConnectorFlag(args []string) (rest []string, name string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--connector" && i+1 < len(args) {
			name = args[i+1]
			i++
			continue
		}
		if v, ok := strings.CutPrefix(a, "--connector="); ok {
			name = v
			continue
		}
		rest = append(rest, a)
	}
	return rest, name
}

func serviceNames() string {
	var names []string
	for _, s := range chat.Services() {
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return "none compiled in"
	}
	return strings.Join(names, ", ")
}

// printBotHelp prints usage for `terva bot`.
func printBotHelp() {
	fmt.Fprintf(os.Stderr, `terva bot — chat-service bridge (connectors: %s)

usage:
  terva bot setup                       provision credentials for the connector
  terva bot status                      show bridge config and whether it's running
  terva bot run [flags]                 run in the foreground (ctrl+c to stop)
  terva bot start [flags]               launch in background, detach, return immediately
  terva bot stop                        sigterm the running background bot, sigkill if needed
  terva bot logs [--follow]             tail the background bot's log file
  terva bot reset                       forget credentials + paired user
  terva bot link <connector.json>       install an external connector (symlink)

run flags (terva bot run / start) — a bot is a full agent by default.
  building blocks (turn off one capability each):
    --no-workspace-tools            drop read/write/edit/bash/grep; keep extensions + MCP (least-privilege)
    --no-ext / --no-extensions      don't load extensions
    --no-mcp                        don't start MCP servers
  composite modes (built from the blocks + an identity):
    --no-tools                      all three blocks (and the skill tool) — no tools at all
    --chat                          no tools at all + a conversational, non-coding identity
    --play                          extensions + MCP only (= --no-workspace-tools) + an embodied identity
  --project / --no-project          project-scoped data + extensions (a separate axis, not a tool toggle)
  --idle-nudge DUR / --idle-prompt TEXT   open a conversation when the chat is quiet (e.g. 30m), with an optional cue
  (also --provider/--model/--persona/--cwd/--approval, as in the tui)

every subcommand accepts --connector NAME to pick the chat service;
with one connector compiled in it is the default. "terva telegram-bot"
and "terva tg" are aliases for "terva bot --connector=telegram".

external connectors are separate executables speaking a small json
protocol on stdio (see docs/connectors.md). installed ones live at
$TERVA_HOME/connectors/<name>/connector.json; --connector-manifest PATH
loads one for a single run while developing it.

telegram setup flow:
  1. talk to @BotFather on telegram, /newbot, copy the token
  2. run "terva bot setup" and paste the token
  3. run "terva bot start" (background) or "terva bot run" (foreground)
  4. send /start to your bot; the first sender claims it

while the bot is running, dm it anything and the message is forwarded
to the agent the same way it would be from the tui. image attachments
are passed to vision-capable models. commands the bot handles
directly: /help, /status, /stop.

config & state (telegram):
  $TERVA_HOME/bot.json       # bot token + paired user (mode 0600)
  $TERVA_HOME/bot.pid        # pid of the running bot (written by run/start)
  $TERVA_HOME/logs/bot.log   # stdout+stderr from "terva bot start"
`, serviceNames())
}

// botStatus prints the connector's config block plus daemon liveness.
func botStatus(svc chat.Service) error {
	if svc.StatusText != nil {
		text, err := svc.StatusText(TervaHome())
		if err != nil {
			return err
		}
		fmt.Println(text)
		if !svc.Configured(TervaHome()) {
			return nil
		}
	}

	pid, alive, _ := chat.IsRunning(TervaHome(), svc.Name)
	switch {
	case alive:
		fmt.Printf("process:      running (pid %d)\n", pid)
	case pid > 0:
		fmt.Printf("process:      stopped (stale pid %d in %s)\n", pid, chat.PIDPath(TervaHome(), svc.Name))
	default:
		fmt.Println("process:      stopped")
	}
	logPath := chat.LogPath(TervaHome(), svc.Name)
	if fi, err := os.Stat(logPath); err == nil {
		fmt.Printf("log file:     %s (%d bytes)\n", logPath, fi.Size())
	}
	return nil
}

// botReset wipes the connector's persisted credentials.
func botReset(svc chat.Service) error {
	if svc.Reset == nil {
		return fmt.Errorf("connector %q has no reset flow", svc.Name)
	}
	removed, err := svc.Reset(TervaHome())
	if err != nil {
		return err
	}
	if removed == "" {
		fmt.Println("no bot config to reset")
		return nil
	}
	fmt.Println("removed", removed)
	return nil
}

// botStart launches `terva bot run` as a detached child process, writes
// its pid file, and returns immediately. Stdout/stderr of the child
// are redirected to the connector's log file.
func botStart(svc chat.Service, rawTail []string) error {
	// Refuse to start if another bot is already running.
	if pid, alive, _ := chat.IsRunning(TervaHome(), svc.Name); alive {
		return fmt.Errorf("bot is already running (pid %d); use `terva bot stop` first", pid)
	}
	_ = chat.RemovePID(TervaHome(), svc.Name) // clear any stale pid file

	if !svc.Configured(TervaHome()) {
		return fmt.Errorf("connector %q is not configured — run `terva bot setup` first", svc.Name)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate terva binary: %w", err)
	}

	logPath := chat.LogPath(TervaHome(), svc.Name)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	// Refuse to start from a `go run` temp binary: Go deletes the
	// binary when `go run` exits, which breaks the detached child.
	// Users hit cryptic tls / exec errors on that path; fail clearly.
	if strings.Contains(self, string(os.PathSeparator)+"go-build") ||
		strings.Contains(self, string(os.PathSeparator)+"go-tmp") {
		return fmt.Errorf("detected `go run` temp binary at %s — run `make install` (or copy ./bin/terva to your PATH) and use the installed binary for `start`", self)
	}

	// Child argv: same flags the user passed to `terva bot start`,
	// mapped to `terva bot run` with the connector pinned. Preserves
	// --provider, --model, --cwd, etc.
	args := append([]string{"bot", "run", "--connector=" + svc.Name}, rawTail...)
	cmd := exec.Command(self, args...)
	// The daemon child re-spawns connectors itself; start it from a
	// sanitized env so injection vars don't ride along (see procenv).
	cmd.Env = procenv.Inherited()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Detach: new session / new process group so terminal signals
	// don't reach the child. Impl lives in botcmd_unix.go /
	// botcmd_windows.go because Setsid is posix-only.
	detachChild(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn: %w", err)
	}
	if err := chat.WritePID(TervaHome(), svc.Name, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("write pid: %w", err)
	}
	// Don't wait() — detach. OS will reparent the child to init when we exit.
	go func() { _ = cmd.Process.Release() }()

	fmt.Printf("started terva bot (%s) as pid %d (logs: %s)\n", svc.Name, cmd.Process.Pid, logPath)
	fmt.Println("use `terva bot logs -f` to tail, `terva bot stop` to stop.")
	return nil
}

// botStop sends SIGTERM to the running bot (SIGKILL if it doesn't
// exit within 5s) and cleans up the pid file.
func botStop(svc chat.Service) error {
	pid, alive, err := chat.IsRunning(TervaHome(), svc.Name)
	if err != nil {
		return err
	}
	if !alive {
		if pid > 0 {
			_ = chat.RemovePID(TervaHome(), svc.Name)
			fmt.Printf("no live process; cleared stale pid %d\n", pid)
			return nil
		}
		fmt.Println("bot is not running")
		return nil
	}
	if err := chat.StopProcess(pid, 5*time.Second); err != nil {
		return fmt.Errorf("stop pid %d: %w", pid, err)
	}
	_ = chat.RemovePID(TervaHome(), svc.Name)
	fmt.Printf("stopped pid %d\n", pid)
	return nil
}

// botLogs prints (or tails with --follow) the bot log file.
func botLogs(svc chat.Service, rawTail []string) error {
	follow := false
	for _, a := range rawTail {
		if a == "-f" || a == "--follow" {
			follow = true
		}
	}
	p := chat.LogPath(TervaHome(), svc.Name)
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("no log file at", p)
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}
	if !follow {
		return nil
	}

	// Naive tail -f: poll for new bytes until ctrl+c.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigc)
	for {
		select {
		case <-sigc:
			return nil
		case <-time.After(500 * time.Millisecond):
			if _, err := io.Copy(os.Stdout, f); err != nil {
				return err
			}
		}
	}
}

// botRun starts the chat-ops loop in the foreground. Ctrl+C stops it.
func botRun(svc chat.Service, rawTail []string, version string) error {
	// Proactive idle nudge (experiment): --idle-nudge <dur> makes the bot open
	// a conversation when the paired chat has been quiet that long; --idle-prompt
	// <text> overrides the cue. Strip them here so the shared parser (which does
	// not know them) never sees them.
	rawTail, idleAfter, idlePrompt := extractIdleNudgeFlags(rawTail)

	// Parse only a small subset of flags relevant to bot run. We reuse
	// the main args parser so --provider/--model/--cwd/--api-key/--reasoning
	// behave the same as in the tui.
	args, err := ParseArgs(rawTail)
	if err != nil {
		return err
	}
	// `terva bot run --help` / -h: print bot usage and stop, rather than
	// falling through and trying to launch (ParseArgs records Help but the run
	// path never checked it).
	if args.Help {
		printBotHelp()
		return nil
	}

	// Project-scoped mode (data in .terva/home, only project extensions; login
	// + trust stay global). Must run before LoadConfig/Resolve read the home.
	if note, perr := maybeEnableProjectScope(args); perr != nil {
		return perr
	} else if note != "" {
		fmt.Fprintln(os.Stderr, "terva:", note)
	}

	// A bot is headless — there is no interactive approval prompt — so default
	// to yolo (run tools) like the other headless modes; otherwise a default
	// bot would refuse every mutating tool and be useless. An explicit
	// --approval / --no-yolo, or a config "approval", still wins. (Jail is left
	// alone: built-in file/shell tools stay confined to the cwd.) Setting this
	// before Resolve keeps the resolver and the gate agreeing.
	cfg, _ := LoadConfig()
	if v := botApprovalDefault(args.Approval, args.NoYolo, cfg.Approval); v != "" {
		args.Approval = v
	}

	// Translate sigterm/sigint into a context cancel so the bot's goroutines,
	// the in-flight turn, and the extension subprocesses wind down cleanly.
	// Created early so extensions are tied to it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigc
		cancel()
	}()

	// Bot mode always requires credentials (can't pop a /login dialog).
	resolved, err := Resolve(args, true)
	if err != nil {
		return err
	}

	// A bot is a full agent, not just a chat box: host extensions + MCP (so the
	// model gets their tools and live context cards) under the same headless
	// gate the print/json modes use. Honors --no-ext / --no-mcp / --no-tools,
	// and the --chat / --play meta-modes (chat → no tools at all; play → the
	// extension/MCP tools, built-in coding tools off) via Resolve + the merge.
	gate, roSet := headlessConfirmGate(args, "bot")
	resolved.AdoptReadOnlySet(roSet)
	extMgr, stopExt := setupNonInteractiveExtensions(ctx, args, &resolved, version)
	defer stopExt()

	conn, pairing, err := svc.NewConnector(TervaHome(), nil)
	if err != nil {
		return err
	}

	agent := resolved.NewAgent()
	wireBotAgentExtHooks(ctx, agent, extMgr, gate, args, &resolved)

	// Session: optional, same model as the tui. Persist so DMs build on
	// prior context. --no-session disables.
	var sess *core.Session
	if !args.NoSess {
		s, _, serr := openOrCreateSessionForBot(args, resolved, agent, version)
		if serr == nil {
			sess = s
			defer sess.Close()
		} else {
			fmt.Fprintln(os.Stderr, "session:", serr)
		}
	}
	// Tell session-keyed extensions the real session id before any turn runs.
	emitSessionStart(extMgr, sess)

	var loop *chat.Loop
	loop = &chat.Loop{
		Connector:  conn,
		Agent:      agent,
		Provider:   resolved.Provider,
		AuthMethod: resolved.AuthMethod,
		CWD:        args.CWD,
		Pairing:    pairing,
		IdleAfter:  idleAfter,
		IdleNudge:  idlePrompt,
		RefreshCreds: func() error {
			// Re-run the same resolver the tui uses so we pick up
			// refreshed oauth tokens, re-logins, and model switches.
			// Only the provider client is swapped — tools, sandbox,
			// system prompt, and transcript stay with the existing agent.
			next, err := Resolve(args, true)
			if err != nil {
				return err
			}
			agent.SetClientAndModel(next.NewClient(), next.Model)
			loop.UpdateStatusContext(next.Provider, next.AuthMethod, next.CWD)
			return nil
		},
	}

	// Record our pid so `terva bot status` / `terva bot stop` can find us,
	// regardless of whether we were started directly or via `bot start`.
	_ = chat.WritePID(TervaHome(), svc.Name, os.Getpid())
	defer chat.RemovePID(TervaHome(), svc.Name)

	// ctx + signal handling were set up at the top so extensions are tied to
	// the same lifecycle; just run the loop until it's cancelled.
	return loop.Run(ctx)
}

// botApprovalDefault returns the approval mode a headless bot should default to
// when the user hasn't chosen one — yolo, so the bot can actually use its tools
// (there is no interactive prompt to confirm at, and the pre-existing bot ran
// tools un-gated). Returns "" to leave the approval unchanged, so an explicit
// --approval flag, --no-yolo, or a config "approval" all win over this default.
func botApprovalDefault(approvalFlag string, noYolo bool, cfgApproval string) string {
	if approvalFlag == "" && !noYolo && cfgApproval == "" {
		return string(core.ApprovalYolo)
	}
	return ""
}

// extractIdleNudgeFlags pulls --idle-nudge <dur>/--idle-nudge=<dur> and
// --idle-prompt <text>/--idle-prompt=<text> out of a raw arg tail, returning
// the remaining args plus the parsed values (zero duration = feature off). A
// malformed duration is ignored (feature stays off) rather than aborting the
// bot — this is an opt-in convenience flag, not a correctness gate.
func extractIdleNudgeFlags(tail []string) (rest []string, idleAfter time.Duration, idlePrompt string) {
	for i := 0; i < len(tail); i++ {
		a := tail[i]
		take := func(flag string) (string, bool) {
			if a == flag {
				if i+1 < len(tail) {
					i++
					return tail[i], true
				}
				return "", true
			}
			if v, ok := strings.CutPrefix(a, flag+"="); ok {
				return v, true
			}
			return "", false
		}
		if v, ok := take("--idle-nudge"); ok {
			if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
				idleAfter = d
			}
			continue
		}
		if v, ok := take("--idle-prompt"); ok {
			idlePrompt = v
			continue
		}
		rest = append(rest, a)
	}
	return rest, idleAfter, idlePrompt
}

// openOrCreateSessionForBot reuses the same logic as interactive mode
// but never prompts (no TTY picker); falls back to latest or new.
func openOrCreateSessionForBot(args Args, r Resolved, ag *core.Agent, version string) (*core.Session, []any, error) {
	if args.Continue {
		if latest := core.LatestSession(TervaHome(), args.CWD); latest != "" {
			s, msgs, err := core.OpenSession(latest)
			if err != nil {
				return nil, nil, err
			}
			for _, w := range s.LoadWarnings {
				fmt.Fprintln(os.Stderr, "terva:", w)
			}
			ag.SetMessages(msgs)
			return s, nil, nil
		}
	}
	s, err := core.NewSession(TervaHome(), args.CWD, r.Provider, r.Model, version)
	return s, nil, err
}
