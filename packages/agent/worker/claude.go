package worker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/agent/talkoot"
)

// tervaExe resolves the path to THIS terva binary. Two callers self-exec it: a
// Claude worker spawns `terva mcp-approval-bridge` as its permission tool, and a
// terva/terva:portable worker IS `terva rpc`. It is a var so a live test can
// point it at a freshly-built terva — os.Executable() inside `go test` is the
// test harness, which carries none of those subcommands.
var tervaExe = os.Executable

// BackendClaude is the name a SpawnRequest carries to get a Claude Code worker.
const BackendClaude = "claude"

func init() { Register(claudeBackend()) }

// claudeBackend drives Claude Code as a long-lived child.
//
// Everything here was probed against the real binary (2.1.209) rather than read
// from documentation — including two flags that WORK BUT ARE ABSENT FROM
// --help, and two event types that appear in no research note and showed up in
// a three-second run. See docs/proposals/archive/external-agent-workers.md.
func claudeBackend() Backend {
	return Backend{
		Name: BackendClaude,
		// Config-opaque. Claude Code inherits nothing from terva's config: not
		// the persona, not the tools, not the trust state. Everything it knows
		// about this job arrives as briefing text, which is why that text is
		// scrubbed and why a leak here is silent rather than loud.
		SelfAssembles: false,
		Command:       claudeCommand,
		Translate:     translateClaude,
		Steer:         steerClaude,
		Interrupt:     interruptClaude,
		ReportsCost:   true,
		Tools:         claudeTools,
		Installed:     claudeInstalled,
		Cursor:        claudeCursor,
		// The identity is already on --append-system-prompt (see claudeCommand),
		// so the opening turn is the WORK alone. Sending Briefing.Text here would
		// tell the model who it is a second time.
		Opening: func(b Briefing) string { return b.Instructions() },
		// Claude carries its approvals over the MCP permission tool, not the rpc
		// wire: the runner serves a socket and claudeCommand points
		// --permission-prompt-tool at a `terva mcp-approval-bridge` reaching it.
		ApprovalSocket: true,
	}
}

// claudeInstalled reports whether a claude binary is on PATH, which is where
// claudeCommand looks for it.
func claudeInstalled() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// claudeCommand builds the child invocation.
//
// The identity rides --append-system-prompt and the work rides the first user
// frame. Not one flag names a terva tool, and not one names terva.
func claudeCommand(d Dispatch) (*exec.Cmd, error) {
	args := []string{
		"-p",
		// The steer channel. Without it the child BLOCKS ON STDIN and warns
		// "no stdin data received in 3s" — a naive spawn pauses three seconds
		// before every dispatch. Holding stdin open is what makes the worker
		// long-lived and steerable rather than one-shot.
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		// stream-json needs --verbose to emit the full event stream.
		"--verbose",
	}

	// The cursor is ours, minted before the process existed. On a revival we
	// hand it back; on a first run we establish it. Either way the agent is
	// recoverable from the moment meta.json is written, which is the whole
	// point of minting instead of scraping.
	if d.Cursor != "" {
		if d.Resuming {
			args = append(args, "--resume", d.Cursor)
		} else {
			args = append(args, "--session-id", d.Cursor)
		}
	}

	if id := d.Briefing.Identity.travellingText(); id != "" {
		args = append(args, "--append-system-prompt", id)
	}
	if m := claudeModel(d.Briefing.Route.Model); m != "" {
		args = append(args, "--model", m)
	}
	if e := claudeEffort(d.Briefing.Route.Effort); e != "" {
		args = append(args, "--effort", e)
	}

	// The approval bridge, when the runner served one: register `terva
	// mcp-approval-bridge` as a stdio MCP server and delegate every permission
	// prompt to it. Verified against claude 2.1.210 — it calls the tool with
	// {tool_name, input} for any file edit or non-safe Bash and honours the
	// {behavior:"allow"|"deny"} reply (a deny blocks the tool, recorded in
	// permission_denials). --strict-mcp-config so the worker's MCP surface is
	// EXACTLY the bridge, never the human's own configured servers.
	canAsk := d.ApprovalSocket != ""
	if canAsk {
		exe, err := tervaExe()
		if err != nil {
			return nil, fmt.Errorf("locate terva for the approval bridge: %w", err)
		}
		cfg, err := json.Marshal(map[string]any{
			"mcpServers": map[string]any{
				mcpbridge.ServerName: map[string]any{
					"command": exe,
					"args":    []string{"mcp-approval-bridge", "--socket", d.ApprovalSocket},
				},
			},
		})
		if err != nil {
			return nil, err
		}
		args = append(args,
			"--mcp-config", string(cfg),
			"--strict-mcp-config",
			"--permission-prompt-tool", mcpbridge.PermissionToolRef,
		)
	}
	// A tools list narrows the built-in set, and --strict-mcp-config bounds
	// the MCP servers on both paths. With the bridge, the block above set it,
	// so the servers are the bridge alone. Without it, the flag with no config
	// loads none. Either way, the person's own servers bring no tool past the
	// list. A non-nil empty list is seat tools alone, and narrows to nothing.
	if d.Tools != nil {
		names, err := claudeTools(d.Tools)
		if err != nil {
			return nil, err
		}
		args = append(args, "--tools", strings.Join(names, ","))
		if !canAsk {
			args = append(args, "--strict-mcp-config")
		}
	}
	if pm := claudePermissionMode(d.Briefing.Policy.Posture, canAsk); pm != "" {
		args = append(args, "--permission-mode", pm)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = d.Dir
	// procenv.Inherited() is the sanitized base every terva child gets: loader
	// injection vars stop here. Claude finds its own credentials the same way it
	// does for a human — we deliberately do NOT hand it ours, and we deliberately
	// do not reach for --bare, which would force ANTHROPIC_API_KEY auth and break
	// a subscription login outright.
	cmd.Env = procenv.Inherited()
	return cmd, nil
}

// claudeToolNames maps terva's core tools onto Claude Code's built-in ones.
var claudeToolNames = map[string]string{
	"read": "Read", "write": "Write", "edit": "Edit",
	"bash": "Bash", "grep": "Grep", "glob": "Glob",
}

// claudeTools maps a tools allowlist onto Claude Code's --tools names. A seat
// tool reaches Claude through the Talkoot MCP bridge, not --tools, so it maps
// to nothing. Any other name, a pattern included, is refused: Claude has no
// tool terva can name for it.
//
// ⚠️ With a list, --strict-mcp-config loads only the servers in --mcp-config.
// The Talkoot MCP bridge (decision 0023) is not built yet. When it is, it
// must join the --mcp-config on both paths, or the seat tools go with the
// person's own servers.
func claudeTools(names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		if slices.Contains(talkoot.SeatTools, n) {
			continue
		}
		c, ok := claudeToolNames[n]
		if !ok {
			return nil, fmt.Errorf("claude has no tool for %q; it can narrow to read, write, edit, bash, grep, and glob", n)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		// --tools "" disables every built-in tool, which is what a list of
		// seat tools alone asks for.
		return []string{""}, nil
	}
	return out, nil
}

// claudeModel maps terva's model id onto Claude's namespace.
//
// Model ids are not portable and never will be: terva may be routed at a Kimi
// or a GPT, and neither name means anything to this CLI. So we do not translate
// — we DECLINE to, and let Claude pick its own default, which is the honest
// answer to "what should a Claude worker run when terva is pointed at gpt-5?".
//
// Only an unambiguous family alias crosses. Claude Code accepts "opus",
// "sonnet", "haiku" as aliases for the current model of that family, so a terva
// run on Anthropic can hand its tier across without pinning a version that may
// not exist over there.
func claudeModel(tervaModel string) string {
	lower := strings.ToLower(tervaModel)
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "opus"):
		return "opus"
	case strings.Contains(lower, "sonnet"):
		return "sonnet"
	case strings.Contains(lower, "haiku"):
		return "haiku"
	}
	// A non-Anthropic model. Say nothing and let the worker use its default.
	return ""
}

// claudeEffort maps terva's reasoning level. The two vocabularies happen to
// coincide exactly (low/medium/high/xhigh/max), which is luck, not design — so
// an unrecognised value is dropped rather than passed through to be rejected by
// the child at spawn.
func claudeEffort(reasoning string) string {
	switch reasoning {
	case "low", "medium", "high", "xhigh", "max":
		return reasoning
	}
	return ""
}

// claudePermissionMode maps terva's approval posture onto Claude's permission
// modes. The mapping is LOSSY BY NATURE and the losses run one way: terva's
// postures are finer than Claude's flags, so every mapping either narrows or
// stays put. None of them widens.
//
// canAsk says whether the approval bridge is wired (the runner served a socket
// and claudeCommand pointed --permission-prompt-tool at it). It is the hinge for
// the "ask" postures: with the bridge, a worker CAN put a permission request in
// front of the orchestrator's human, so ask/workspace run in Claude's default
// mode where every non-safe tool call is delegated to the bridge. Without it,
// the worker cannot ask anyone, and the only safe reading of an unhonorable
// "ask" is the most restrictive mode available (plan) — never a permissive one.
// A worker that silently stopped asking and started acting would be the worst
// failure this design has.
func claudePermissionMode(posture string, canAsk bool) string {
	switch posture {
	case "yolo":
		// The dispatcher explicitly chose un-gated execution, and the worker is
		// confined to a leased worktree. bypassPermissions skips the permission
		// tool entirely — no card for a run that was declared un-gated. (Before
		// the bridge this was acceptEdits, a half-measure that still left non-edit
		// tools with no way to be answered; now yolo means yolo.)
		return "bypassPermissions"
	case "plan":
		return "plan"
	case "auto-edit":
		// Edits are auto-approved; every other tool consults the bridge if wired,
		// else falls back to plan (it can neither act nor ask).
		if canAsk {
			return "acceptEdits"
		}
		return "plan"
	case "ask", "workspace":
		if canAsk {
			// Default mode: non-safe tool calls route to the bridge → the human.
			return ""
		}
		// No bridge: give it the mode that lets it think and read without acting,
		// rather than quietly promoting it to acceptEdits.
		return "plan"
	}
	return ""
}

// claudeCursor mints a resume token before the child exists.
//
// A uuid, because --session-id demands one — but DERIVED from the agent id
// rather than random. Deriving it means the cursor can be recomputed from the
// agent alone, so an agent whose meta.json was lost, or written before a crash,
// is still recoverable: the cursor is a function of its identity, not a fact
// that had to survive.
func claudeCursor(agentID string) string {
	sum := sha256.Sum256([]byte("terva-worker-claude\x00" + agentID))
	// Stamp the RFC-4122 version (4) and variant bits so the CLI's uuid
	// validation accepts it.
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(sum[0:4]),
		binary.BigEndian.Uint16(sum[4:6]),
		binary.BigEndian.Uint16(sum[6:8]),
		binary.BigEndian.Uint16(sum[8:10]),
		sum[10:16])
}

// steerClaude encodes a follow-up turn as a stream-json input frame — the same
// shape the Anthropic API uses for a user message, which is what the CLI expects
// on stdin under --input-format stream-json.
func steerClaude(text string) ([]byte, error) {
	frame := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
	line, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// interruptClaude encodes a stream-json control request that stops the child's
// in-flight turn and leaves the process running. A probe of 2.1.280 on
// 2026-09-24 confirmed the frame. The child acknowledges it with a
// control_response line, then ends the turn with a result whose
// terminal_reason is "aborted_streaming".
//
// The request id only has to be unique within this child, and the runner never
// reads the acknowledgement, so a random id is enough.
func interruptClaude() ([]byte, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	frame := map[string]any{
		"type":       "control_request",
		"request_id": "terva-interrupt-" + hex.EncodeToString(id[:]),
		"request":    map[string]any{"subtype": "interrupt"},
	}
	line, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// claudeInterrupted is the terminal_reason of a turn that an interrupt stopped.
const claudeInterrupted = "aborted_streaming"

// translateClaude maps one stdout line into terva's swarm vocabulary.
//
// It is a pure function of the line, which is what lets a captured stream be
// replayed through it as a golden fixture — and lets a stream recorded under one
// CLI version be re-translated after an upgrade, instead of being lost to it.
//
// The default case is the load-bearing one. Two of the five event types this CLI
// emitted in a three-second run appear in NO research note (rate_limit_event,
// system/thinking_tokens). An exhaustive switch over a closed set would already
// be dropping events from a vendor that ships weekly — so anything terva does not
// model becomes transcript rather than nothing. The raw line is retained by the
// runner regardless: a translator is never the reason something is lost.
func translateClaude(line []byte) []Event {
	var ev struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Message json.RawMessage `json:"message"`
		Result  string          `json:"result"`
		IsError bool            `json:"is_error"`

		SessionID     string          `json:"session_id"`
		Model         string          `json:"model"`
		CLIVersion    string          `json:"claude_code_version"`
		NumTurns      int             `json:"num_turns"`
		TotalCostUSD  float64         `json:"total_cost_usd"`
		DurationMS    int             `json:"duration_ms"`
		StopReason    string          `json:"stop_reason"`
		TermReason    string          `json:"terminal_reason"`
		Usage         json.RawMessage `json:"usage"`
		PermDenials   json.RawMessage `json:"permission_denials"`
		ThinkingToken int             `json:"estimated_tokens"`
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
		return nil // not an event; the runner keeps the raw line as transcript
	}

	switch ev.Type {
	case "system":
		switch ev.Subtype {
		case "init":
			// The version stamp arrives here, free, in the first event of the run
			// we were doing anyway — no second process, and it is the version that
			// ACTUALLY RAN rather than one a separate probe reported.
			return []Event{{Type: "agent_ready", Data: map[string]any{
				"backend":    BackendClaude,
				"version":    ev.CLIVersion,
				"model":      ev.Model,
				"session_id": ev.SessionID,
			}}}
		case "thinking_tokens":
			return nil // pure telemetry; the raw line is kept, nothing to model
		}
		return nil

	case "assistant":
		// Near-identity: Claude's `message` is an Anthropic message with
		// content[] blocks, and terva's applyEventToSink already reads exactly
		// that shape (data.message.content[]). Nothing to reshape — the two
		// vocabularies agree because they descend from the same API.
		if len(ev.Message) == 0 {
			return nil
		}
		var msg map[string]any
		if err := json.Unmarshal(ev.Message, &msg); err != nil {
			return nil
		}
		return []Event{{Type: "assistant_message", Data: map[string]any{"message": msg}}}

	case "user":
		if len(ev.Message) == 0 {
			return nil
		}
		var msg map[string]any
		if err := json.Unmarshal(ev.Message, &msg); err != nil {
			return nil
		}
		return []Event{{Type: "user_message", Data: map[string]any{"message": msg}}}

	case "result":
		// The terminal event, and the one that has no analog abroad: a foreign
		// worker does not emit task_end, it stops talking. This is where terva
		// synthesizes one — plus the cost accounting, which needs no invention
		// because the envelope already carries it.
		data := map[string]any{
			"step":        ev.NumTurns,
			"cost_usd":    ev.TotalCostUSD,
			"duration_ms": ev.DurationMS,
			"stop_reason": ev.StopReason,
		}
		if ev.TermReason != "" {
			data["terminal_reason"] = ev.TermReason
		}
		switch {
		case ev.IsError:
			data["error"] = firstNonEmpty(ev.Result, "the worker reported a failure without saying what it was")
		case ev.TermReason == claudeInterrupted:
			// 🚨 An interrupted turn still reports subtype "success" and
			// stop_reason "end_turn". Only terminal_reason tells it apart, and
			// without an error here the supervisor would record a finished task.
			// A native child's cancelled turn carries an error the same way.
			data["error"] = "the turn was interrupted"
		}
		if len(ev.Usage) > 0 {
			var usage map[string]any
			if json.Unmarshal(ev.Usage, &usage) == nil {
				data["usage"] = usage
			}
		}
		return []Event{{Type: "task_end", Data: data}}

	case "rate_limit_event":
		return nil // telemetry we do not yet model; retained raw

	case "control_response":
		return nil // the acknowledgement of an interrupt; the result that follows ends the turn
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
