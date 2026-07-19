package worker

import (
	"fmt"
	"os/exec"
	"sort"
	"sync"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
)

// Backend is how to drive one coding agent that is not terva — the moral
// equivalent of a providerSpec row, and registered the same way.
//
// It is a spec, not an interface, for the reason providerSpec is: a backend is
// a handful of decisions, not a hierarchy, and a struct of functions makes the
// full surface of "what a backend must answer for" readable in one screen.
// Anything a backend does NOT need to customise, it leaves nil and inherits.
//
// The contract was extracted from what ONE real backend needs, driven against
// the actual binary (see docs/proposals/external-agent-workers.md, "Probed
// against the real CLI"). It is deliberately not more general than that. A
// second backend will bend it, and bending it against a real second case beats
// guessing at one now.
type Backend struct {
	// Name is the label a SpawnRequest carries. It is what the swarm persists,
	// so renaming one strands every agent already spawned on it.
	Name string

	// SelfAssembles splits the table in two, and it is the whole asymmetry this
	// package exists to manage.
	//
	// TRUE (config-transitive — terva driving terva): the worker re-derives
	// persona, tools, project instructions, skills, lore, and trust FROM THE
	// SAME DISK AND CONFIG, because it is the same harness. Capability parity
	// IS config parity. The parent passes flags and a task, never a rendered
	// prompt.
	//
	// FALSE (config-opaque — every foreign agent): it inherits nothing from us.
	// Its capability comes from its own install; terva can NARROW it with policy
	// flags but cannot extend it, and terva's config means nothing to it.
	// Everything it learns from us arrives as briefing text — which is exactly
	// why that text is scrubbed.
	SelfAssembles bool

	// Command builds the child process. The Briefing is already composed and
	// scrubbed; this renders it into argv, env, and stdin for one vendor's CLI.
	Command func(d Dispatch) (*exec.Cmd, error)

	// Translate maps ONE line of the child's stdout into zero or more events in
	// terva's swarm vocabulary. Returning no events is normal and means "nothing
	// terva models" — the raw line is retained regardless, so a translator is
	// never the reason something is lost.
	//
	// It must be a PURE FUNCTION of the line. That is what lets a captured
	// stream be replayed through it as a golden fixture, and what lets a stream
	// recorded under one CLI version be re-translated after an upgrade.
	Translate func(line []byte) []Event

	// Opening is the text of the FIRST user turn — the one that delivers the
	// task, sent on the child's stdin right after it starts (and only on a first
	// run; a revival's session already holds it). It exists because a backend
	// splits the briefing across two channels its own way: Claude Code carries
	// the identity in --append-system-prompt, so its opening turn is the WORK
	// alone (Briefing.Instructions) and repeating the identity here would say it
	// twice. Nil means "send the whole briefing" (Briefing.Text) — the right
	// default for a backend with no separate system-prompt channel.
	//
	// The returned string is encoded into a stdin frame by Steer, so a backend
	// that sets Opening must also set Steer.
	Opening func(b Briefing) string

	// Steer encodes a follow-up user turn as a frame on the child's stdin. Nil
	// means the backend cannot be steered once started, and the supervisor will
	// say so rather than silently dropping the message. The runner also uses it
	// to encode the opening turn (see Opening).
	Steer func(text string) ([]byte, error)

	// RecognizeAsk inspects a translated event and, if it is an approval request
	// from the worker, returns the Ask and true. Nil means this backend never
	// asks for approval over its event stream — its worker either cannot be
	// gated interactively, or (claude, terva:portable) rides a different carrier
	// (an MCP permission tool). Only the config-transitive terva backend asks on
	// the wire, because only it speaks terva's own rpc ask/approve protocol.
	//
	// When set, the runner routes each Ask to the orchestrator's Confirmer (the
	// human's card) and replies with EncodeApprove, so a worker's tool-approval
	// reaches the same place the orchestrator's own tool approvals do.
	RecognizeAsk func(ev Event) (Ask, bool)

	// EncodeApprove encodes a decision as the reply frame for the child's stdin,
	// correlated to the Ask's id. Required when RecognizeAsk is set.
	EncodeApprove func(askID string, d core.ConfirmDecision) ([]byte, error)

	// ApprovalSocket says this backend gates tool use through terva's MCP
	// approval bridge rather than the rpc-native ask carrier. When true, the
	// runner opens a per-worker unix socket (0600 — perms are the auth), serves
	// approval questions from it (routing to the orchestrator's Confirmer, the
	// SAME terminus RecognizeAsk uses), and hands its path to Command as
	// Dispatch.ApprovalSocket. Command then points the CLI's permission tool at a
	// `terva mcp-approval-bridge --socket <path>` (claude's --permission-prompt-
	// tool, terva:portable's --approval-tool).
	//
	// Mutually exclusive with RecognizeAsk: a backend asks over ITS wire or over
	// the socket, never both. terva (config-transitive) uses the wire; claude and
	// terva:portable (config-opaque) use the socket.
	ApprovalSocket bool

	// Cursor mints the resume token BEFORE the process starts, from the agent
	// id. Nil means the backend cannot resume.
	//
	// Minting rather than scraping is not a style preference. A child that dies
	// before it announces its session id — a bad flag, a missing credential, an
	// OOM — would leave a scraped-cursor agent with no cursor and therefore no
	// way back. Minting means the cursor is durable before there is a process to
	// lose. (Claude Code takes `--session-id <uuid>`; verified against 2.1.209.)
	Cursor func(agentID string) string
}

// Dispatch is everything Command needs to build a child: what to say, where to
// say it, and under what constraints.
type Dispatch struct {
	// Briefing is the composed, scrubbed payload. For a SelfAssembles backend
	// it is mostly ignored — that worker re-derives its own context — and the
	// Task is what crosses.
	Briefing Briefing

	// Dir is the leased worktree the child runs in.
	Dir string

	// Cursor is the resume token, minted at spawn. Resuming says whether this
	// dispatch is a revival (use the cursor) or a first run (establish it).
	Cursor   string
	Resuming bool

	// ApprovalSocket is the unix-socket path the worker's MCP approval bridge
	// dials to reach the orchestrator's Confirmer. Non-empty only for a backend
	// with ApprovalSocket set (and only when the runner could open it); Command
	// puts a `terva mcp-approval-bridge --socket <path>` on the worker's MCP
	// config and names its tool to the CLI's permission flag. Empty means "no
	// approval carrier wired" — the worker's own headless default (deny) applies.
	ApprovalSocket string

	// SessionPath is the per-agent session file the worker persists its
	// conversation to (the swarm's <root>/agents/<id>/session.json). A terva
	// backend passes it as `terva rpc --session <path>`, so the worker's rpc
	// session is durable: it is created on first run and REOPENED on a revival,
	// which — paired with Resuming suppressing the re-sent opening turn — is how a
	// terva worker resumes with its transcript intact. This is terva's resume
	// mechanism (a session file it owns), the analog of the claude backend's
	// minted Cursor. Empty means no persistence (the process holds the
	// conversation in memory only, and a revival starts blank).
	SessionPath string
}

// Event is one thing that happened, in terva's swarm vocabulary rather than a
// vendor's. Type and Data mirror swarm.Event's shape deliberately: the swarm
// package must not import this one (it is a supervisor and knows nothing about
// backends), so the runner that bridges them does the conversion.
type Event struct {
	Type string
	Data map[string]any
}

// Ask is one tool-approval request a worker surfaced — the wire-level analog of
// a core.Confirmer prompt. The runner routes it to the orchestrator's human and
// replies with the backend's EncodeApprove, correlated by ID.
type Ask struct {
	ID      string // the backend's correlation id, echoed back in the reply
	Tool    string // the tool the worker wants to run
	Preview string // a one-line summary of its arguments
}

var (
	mu       sync.RWMutex
	registry = map[string]Backend{}
)

// Register adds a backend to the table. It panics on a duplicate name, because
// a silently-shadowed backend would route agents to the wrong runner and there
// is no recovering from that at runtime.
func Register(b Backend) {
	mu.Lock()
	defer mu.Unlock()
	if b.Name == "" {
		panic("worker: backend with no name")
	}
	if _, dup := registry[b.Name]; dup {
		panic("worker: duplicate backend " + b.Name)
	}
	registry[b.Name] = b
}

// Lookup resolves a backend by name.
//
// An unknown name is an ERROR, never a fallback to native. A dispatch that
// asked for a Claude worker and silently got a terva one would run, and produce
// plausible work, and be wrong about what it was — the exact failure this design
// spends all its effort avoiding elsewhere.
func Lookup(name string) (Backend, error) {
	mu.RLock()
	defer mu.RUnlock()
	b, ok := registry[name]
	if !ok {
		return Backend{}, fmt.Errorf("worker: unknown backend %q (known: %v)", name, namesLocked())
	}
	return b, nil
}

// Names lists the registered backends, sorted. The `swarm_spawn` tool's backend
// enum is built from this, so a backend that is not registered is not offerable
// — the schema simply does not mention it, and no model can ask for it.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	return namesLocked()
}

func namesLocked() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AllowSpawn reports whether a foreign worker backend may be spawned right now:
// external workers must be enabled AND the backend registered. It is the single
// gate every foreign spawn consults — the swarm_spawn tool (via workspace's
// allowWorkerBackend), the board's tasks-surface spawn, and the TUI's /swarm
// command — so the policy cannot drift between initiators. The config knob is
// read LIVE, so toggling it applies to the next spawn without a rebuild. Native
// spawns carry an empty name and must never reach here; this is for foreign only.
func AllowSpawn(name string) error {
	if !config.ExternalWorkersEnabled() {
		return fmt.Errorf("external workers are off — enable external_workers before dispatching to %q", name)
	}
	if _, err := Lookup(name); err != nil {
		return err
	}
	return nil
}
