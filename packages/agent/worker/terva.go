package worker

import (
	"encoding/json"
	"os/exec"
	"strings"

	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/core"
)

// BackendTerva is the name a SpawnRequest carries to get a terva-driving-terva
// worker — the dogfood backend.
const BackendTerva = "terva"

// BackendTervaPortable drives terva as a config-OPAQUE worker: the composer's
// sufficiency test. It routes through the identical foreign composer as Claude
// Code, so the briefing it carries is byte-identical to Claude's — but the
// worker is `terva rpc --portable`, which strips terva's harness-local
// self-context so terva runs on ONLY what the briefing gave it. If terva can do
// the task natively but fails on the portable briefing, we stripped something
// load-bearing — the one failure the leak scrub cannot catch.
const BackendTervaPortable = "terva:portable"

func init() {
	Register(tervaBackend())
	Register(tervaPortableBackend())
}

// tervaBackend drives terva itself through its PUBLIC rpc wire — no
// `--swarm-agent` flag, no inbox socket, no private knowledge — the way a
// stranger's orchestrator would. It is the config-TRANSITIVE half of the
// contract, and it exists to prove the public wire suffices and to be the
// composer's control case: it renders the OPPOSITE way from a foreign backend.
//
// SelfAssembles = true is the whole asymmetry. A terva worker re-derives its
// persona, conventions, tools, AGENTS.md, skills, and trust state from THE SAME
// CONFIG the parent read — because it is the same harness — so we send it none
// of that. Only the task crosses (as an rpc `prompt`), and the model, provider,
// approval, and persona cross as FLAGS the child resolves for itself. The
// harness-local segments a foreign renderer strips are correct to keep here,
// because here they are true: this worker really does have edit/write, really
// does render to a terva surface, really is governed by terva's trust model.
func tervaBackend() Backend {
	return Backend{
		Name:          BackendTerva,
		SelfAssembles: true,
		Command:       tervaCommand,
		Translate:     translateTerva,
		Steer:         steerTerva,
		// The identity rides --persona (the child self-assembles it), so the
		// opening turn is the task and the reporting contract alone — no
		// workspace line, no pointers. See Briefing.selfAssembledTask.
		Opening: Briefing.selfAssembledTask,
		// The rpc-native approval carrier: a gated terva worker emits `ask`
		// frames (because tervaCommand passes --rpc-approvals), and the runner
		// routes them to the orchestrator's human and replies with `approve`.
		RecognizeAsk:  recognizeTervaAsk,
		EncodeApprove: encodeTervaApprove,
		// Cursor is nil: terva does not resume via a minted token like claude.
		// Its resume mechanism is a session FILE it owns — tervaCommand passes
		// Dispatch.SessionPath as `terva rpc --session <path>`, so the worker
		// persists and reopens its transcript across a revival (see backend.go's
		// SessionPath). Resuming (from the swarm) suppresses the re-sent opening.
	}
}

// tervaPortableBackend drives `terva rpc --portable` through the CONFIG-OPAQUE
// path — SelfAssembles=false, so it composes and scrubs the identical foreign
// briefing Claude Code gets and hands terva only that. It shares terva's rpc
// wire (translate, steer) but differs where the sufficiency test needs it to:
// the identity rides --system-prompt (not --persona), the opening turn is the
// foreign Instructions (task + workspace + pointers, not just the task), and
// --portable strips terva's own self-context on the worker side so the briefing
// is all it has. The claim this makes testable: the briefing text is
// byte-identical across claude and terva:portable; only the carrier differs.
func tervaPortableBackend() Backend {
	return Backend{
		Name:          BackendTervaPortable,
		SelfAssembles: false, // the identical foreign path as claude — scrub runs
		Command:       tervaPortableCommand,
		Translate:     translateTerva, // same rpc wire as the dogfood backend
		Steer:         steerTerva,
		Opening:       Briefing.Instructions, // the foreign work turn, exactly as claude
		// Approvals ride the MCP bridge, NOT the rpc-native ask carrier: the
		// config-opaque worker gates through terva's own MCP client calling `terva
		// mcp-approval-bridge`, the identical carrier claude uses. This is the
		// conformance parallel — same bridge, same socket — so the sufficiency test
		// covers approvals too. tervaPortableCommand points --approval-socket at
		// the path the runner served here.
		ApprovalSocket: true,
	}
}

// tervaCommand builds `terva rpc` in the leased worktree. It spawns THIS binary
// (os.Executable) — terva driving terva — and passes the model selection,
// approval posture, and persona as flags the child resolves against the same
// config. Not one flag carries a rendered prompt; the task is the first rpc
// `prompt` on stdin (the runner sends it via Steer).
func tervaCommand(d Dispatch) (*exec.Cmd, error) {
	exe, err := tervaExe()
	if err != nil {
		return nil, err
	}
	// --rpc-approvals opts the worker into the ask/approve carrier: a tool call
	// that needs confirmation asks over the wire (which the runner routes to the
	// orchestrator's human) instead of the headless default of refusing. Safe
	// unconditionally because the runner always answers an ask — routing it to a
	// human when one is watching, denying it cleanly when none is — so the worker
	// can never hang on it.
	args := []string{"rpc", "--rpc-approvals", "--cwd", d.Dir}
	// The per-agent session file makes the worker resumable: `terva rpc` creates
	// it on first run and reopens it on a revival (openOrCreateSession), so the
	// transcript survives the process dying. Paired with Resuming suppressing the
	// re-sent opening turn, this is terva's resume — the analog of claude's
	// minted --session-id cursor.
	if d.SessionPath != "" {
		args = append(args, "--session", d.SessionPath)
	}
	if p := strings.TrimSpace(d.Briefing.Route.Provider); p != "" {
		args = append(args, "--provider", p)
	}
	if m := strings.TrimSpace(d.Briefing.Route.Model); m != "" {
		args = append(args, "--model", m)
	}
	if e := strings.TrimSpace(d.Briefing.Route.Effort); e != "" {
		args = append(args, "--reasoning", e)
	}
	// The posture crosses VERBATIM — no mapping, no narrowing. terva's own
	// --approval takes terva's own posture, which is the point of the dogfood
	// backend: the lossy translation a foreign backend must do (claudePermission-
	// Mode) simply does not exist here.
	if a := strings.TrimSpace(d.Briefing.Policy.Posture); a != "" {
		args = append(args, "--approval", a)
	}
	// The persona is a flag the child self-resolves. (A path/card-based identity
	// whose display name is not a resolvable persona name is a known gap for the
	// dogfood backend — the common built-in-name case round-trips.)
	if id := strings.TrimSpace(d.Briefing.Identity.Name); id != "" {
		args = append(args, "--persona", id)
	}

	cmd := exec.Command(exe, args...)
	cmd.Dir = d.Dir
	// The child's stdin is a private pipe — only the runner writes it — so the
	// rpc auth token is unnecessary, and its ABSENCE is what lets the first
	// `prompt` work without a hello handshake (rpc.go: requireToken := token
	// != ""). Strip any token the parent inherited so the child never flips
	// into auth-required mode and waits for a hello we do not send.
	cmd.Env = withoutRPCToken(procenv.Inherited())
	return cmd, nil
}

// tervaPortableCommand builds `terva rpc --portable` in the lease. Unlike the
// dogfood tervaCommand it carries the composed IDENTITY on --system-prompt (the
// same string claude puts on --append-system-prompt, which is what makes the
// briefing byte-identical) and passes NO --persona: the worker must run on the
// briefing, not re-derive its own identity from config. --portable strips its
// harness-local self-context so the briefing is genuinely all it has.
func tervaPortableCommand(d Dispatch) (*exec.Cmd, error) {
	exe, err := tervaExe()
	if err != nil {
		return nil, err
	}
	args := []string{"rpc", "--portable", "--cwd", d.Dir}
	// Resumable via its own session file, same as the dogfood backend (see
	// tervaCommand). --portable strips terva's self-context but not its session
	// persistence, so a revived portable worker still reopens its transcript.
	if d.SessionPath != "" {
		args = append(args, "--session", d.SessionPath)
	}
	if id := d.Briefing.Identity.travellingText(); id != "" {
		args = append(args, "--system-prompt", id)
	}
	if p := strings.TrimSpace(d.Briefing.Route.Provider); p != "" {
		args = append(args, "--provider", p)
	}
	if m := strings.TrimSpace(d.Briefing.Route.Model); m != "" {
		args = append(args, "--model", m)
	}
	if e := strings.TrimSpace(d.Briefing.Route.Effort); e != "" {
		args = append(args, "--reasoning", e)
	}
	if a := strings.TrimSpace(d.Briefing.Policy.Posture); a != "" {
		args = append(args, "--approval", a)
	}
	// The MCP approval carrier: point terva rpc at the socket the runner served,
	// so its confirm gate routes through the bridge to the orchestrator's human —
	// the identical carrier claude gets via --permission-prompt-tool.
	if d.ApprovalSocket != "" {
		args = append(args, "--approval-socket", d.ApprovalSocket)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = d.Dir
	cmd.Env = withoutRPCToken(procenv.Inherited())
	return cmd, nil
}

// withoutRPCToken drops the rpc auth-token vars from an environment, so a child
// terva rpc starts already-authed and accepts the first prompt directly.
func withoutRPCToken(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		// Both spellings, mirroring rpc.go's rpcAuthToken which reads either.
		if strings.HasPrefix(kv, "TERVACORE_RPC_TOKEN=") || strings.HasPrefix(kv, "ZOTCORE_RPC_TOKEN=") { // rename:keep — embedder compat
			continue
		}
		out = append(out, kv)
	}
	return out
}

// steerTerva encodes a follow-up turn as an rpc `prompt` command — the same
// command that delivers the opening task, because to terva a follow-up IS just
// another prompt. No id: the child's `response` ack to it is an rpc protocol
// frame the translator drops anyway.
func steerTerva(text string) ([]byte, error) {
	line, err := json.Marshal(map[string]any{"type": "prompt", "message": text})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// translateTerva maps one rpc stdout line into terva's swarm vocabulary.
//
// This is the near-identity the dogfood backend exists to demonstrate: the rpc
// event stream IS core.WireEvent — the exact vocabulary the swarm event log
// already stores — so the translator's whole job is to unwrap the two rpc
// PROTOCOL frame kinds and pass the events through untouched. Because there is
// almost no mapping, a conformance failure indicts the CONTRACT or the product
// surface, never the translation.
func translateTerva(line []byte) []Event {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil || head.Type == "" {
		return nil
	}

	switch head.Type {
	case "response":
		// An rpc command ack (hello / prompt-started / abort). Protocol, not an
		// agent event — the runner keeps the raw line, nothing to model.
		return nil
	case "done":
		// The per-prompt terminal. There is no task_end on the rpc wire — `done`
		// is it — so synthesize the task-level event the swarm's OnTurnEnd keys
		// on, exactly as the claude backend synthesizes one from `result`. A
		// native swarm child emits task_end per ag.Prompt; this makes the dogfood
		// worker fire OnTurnEnd per prompt the same way.
		return []Event{{Type: "task_end", Data: map[string]any{}}}
	default:
		// Everything else is already a swarm event. Pass it through whole; Type
		// is carried on the Event, so drop the duplicate from Data (swarm.Event's
		// MarshalJSON re-adds it).
		var data map[string]any
		if json.Unmarshal(line, &data) != nil {
			return nil
		}
		delete(data, "type")
		return []Event{{Type: head.Type, Data: data}}
	}
}

// recognizeTervaAsk extracts an approval request from a translated `ask` event —
// the frame `terva rpc --rpc-approvals` emits when a tool call needs
// confirmation. translateTerva passes `ask` through as an ordinary event (it is
// neither a `response` ack nor `done`), so it arrives here with its fields
// intact under Event.Data.
func recognizeTervaAsk(ev Event) (Ask, bool) {
	if ev.Type != "ask" {
		return Ask{}, false
	}
	id, _ := ev.Data["id"].(string)
	if id == "" {
		return Ask{}, false // an ask with no id cannot be answered
	}
	tool, _ := ev.Data["tool"].(string)
	preview, _ := ev.Data["preview"].(string)
	return Ask{ID: id, Tool: tool, Preview: preview}, true
}

// encodeTervaApprove encodes a decision as the rpc `approve` command the worker
// is blocked waiting for, correlated to the ask id. The field names match the
// server's dispatch("approve") reader.
func encodeTervaApprove(askID string, d core.ConfirmDecision) ([]byte, error) {
	line, err := json.Marshal(map[string]any{
		"type":          "approve",
		"id":            askID,
		"allow":         d.Allow,
		"reason":        d.Reason,
		"remember_tool": d.RememberTool,
		"remember_all":  d.RememberAll,
	})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}
