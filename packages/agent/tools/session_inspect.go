package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// SessionInspectTool gives the agent a bounded, filterable view over a session's
// own JSONL transcript — the structured counterpart to the flattened role+text
// an out-of-tree search index sees. It answers "what happened / what went wrong
// in this session" (failed or oversized tool results, by tool) without exposing
// the raw transcript path (which the sandbox jails) or any other file under
// $TERVA_HOME: a session id is resolved inside the active project's sessions
// directory — or, when it names a swarm sub-agent, inside the swarm state root,
// confined to children spawned from this project — and rejected if it tries to
// escape.
type SessionInspectTool struct {
	TervaHome string
	CWD       string
	// Agent is the fallback conversation inspected when the dispatch context
	// carries no agent (direct calls, tests). Bound after the agent is built,
	// with the same ctx-wins semantics as terva_status.
	Agent *core.Agent
	// Sandbox is the SAME read policy the read tool applies, and it is what
	// bounds the `path` argument. Holding the tool to read's reach — no more, no
	// less — is what makes an arbitrary path safe to accept: see resolveFromPath.
	// Nil is the unjailed default and permits everything, exactly as it does for
	// read.
	Sandbox *Sandbox
}

// sessionInspectArgs is the wire shape. Every optional field here is INERT at
// its zero value, and that is load-bearing rather than incidental.
//
// Cursor and Expand were both *int, and both chose behaviour by PRESENCE: a nil
// Expand listed and a non-nil one expanded; a nil Cursor gave the most recent
// window and `cursor: 0` paged from the oldest. A model that fills every
// optional key in a schema — a common and entirely reasonable habit, since JSON
// Schema gives it no way to see that presence is what matters — could therefore
// not reach list mode at all, and could silently be handed the wrong end of the
// transcript. That is exactly what happened: four rejected calls in a row, each
// carrying {"cursor":0,"expand":0,...}, until the agent gave up and wrote its
// review without the evidence it had gone looking for.
//
// The same root cause had already been diagnosed once (TW-013 F2 named this
// very field) and answered with a better error message, which the model could
// not act on because the correction it asked for was an OMISSION. So the fix
// here is representational, not diagnostic: the indices are 1-based on the
// wire, 0 means "unset", and a fully padded call lands on the sensible default
// instead of an error.
type sessionInspectArgs struct {
	SessionID string `json:"session_id"`
	// Path names a session-shaped JSONL anywhere the READ tool can reach it —
	// a transcript the user downloaded, one copied out of another checkout, one
	// written by a different machine. See resolveFromPath for why this grants no
	// access the model does not already have.
	Path         string   `json:"path"`
	EventKinds   []string `json:"event_kinds"`
	ToolName     string   `json:"tool_name"`
	FailuresOnly bool     `json:"failures_only"`
	Limit        int      `json:"limit"`
	Cursor       int      `json:"cursor"`
	Expand       int      `json:"expand"`
	TextOffset   int      `json:"text_offset"`
	Stats        bool     `json:"stats"`
}

// The wire indices are 1-based (#1 is the first match) and the scan's are
// 0-based, because every ring and window computation below is an offset. These
// two convert at the boundary, which is the only place the bases meet.
//
// Cursor and Expand must move together: the listing prints an event's #n and
// cursor is an offset into that same match sequence, so the "more: pass cursor
// N" hint round-trips a coordinate a caller reads off a listing line. Rebasing
// one without the other would put an off-by-one straight into that loop.

// wireIndex renders a 0-based scan index as the 1-based coordinate a caller sees.
func wireIndex(idx int) int { return idx + 1 }

// cursorOffset converts a wire cursor to a scan offset. Nil means "the most
// recent window" — the default a caller gets by saying nothing, or by saying 0.
// A negative cursor cannot mean a page before the beginning, so it reads the
// same way rather than erroring on a value with no other sensible meaning.
func cursorOffset(cursor int) *int {
	if cursor <= 0 {
		return nil
	}
	off := cursor - 1
	return &off
}

// expandTarget converts a wire expand to a scan index. Nil is list mode. A
// positive index is 1-based and rebases; a negative one counts from the end and
// is unchanged, so -1 is still the most recent match.
func expandTarget(expand int) *int {
	if expand == 0 {
		return nil
	}
	idx := expand
	if idx > 0 {
		idx--
	}
	return &idx
}

func (t *SessionInspectTool) Name() string { return "session_inspect" }

// sessionInspectDesc is the English default for
// tool.session_inspect.description. A const for the same reason memoryDesc
// is one: the extractor resolves a named const, not an inline concatenation.
const sessionInspectDesc = "Examine the transcript of this session in a structured form, with a limit on the output. Use this tool to see what occurred, and do not read the full transcript again. Each argument is optional, and a value of 0 means that you did not set it. Therefore you can safely send all the arguments as zeros, which gives the default list.\n\n" +
	"Stats mode occurs when you set stats to true. Use this mode first when you want to know what occurred in this session, or what the session cost. The mode gives one summary of the full session: the cost, the cache hit rate, the dead turns, the counts of tool calls and failures, and the provider errors. To calculate these numbers from the events is expensive.\n\n" +
	"List mode and expand mode are mutually exclusive, and they use the same filters. The filter failures_only selects the tool results that failed. The filter tool_name selects one tool. The filter event_kinds selects some of \"tool_call\", \"tool_result\", \"message\", \"usage\", and \"error\".\n\n" +
	"List mode is the default, and it occurs when expand is 0. The mode shows a window of the events that agree with the filters: the tool calls, the tool results with their pass or fail status, the text of messages, and the usage of each turn. The most recent events are the default. A usage event gives the cost of a turn, and the quantity of its input that came from the prefix cache. Use event_kinds with \"usage\" alone to find where this session spent money, because no other part of the transcript gives this. Expand a usage event to see its full token counts.\n\n" +
	"To move through the list, use limit and cursor. The default limit is 40, and the maximum is 200. The cursor is a position in the events that agree with the filters, and the oldest event is position 1. A cursor of 0 gives the most recent window. The tool returns next_cursor when more events remain, and you must then use the same filters. Each event in the list has an index that starts at #1.\n\n" +
	// The prohibition leads this section, and the section leads with the
	// task that selects the mode. Both orderings are load-bearing, and the
	// A/B measured what happens without them: on Haiku, 20 of 20 runs used
	// expand mode correctly when the old description SHOUTED the rule in
	// capitals, against 10 of 20 once the capitals went. Six of the ten
	// failures set expand AND limit, which the tool refuses; the other four
	// never reached expand mode at all and asked for a one-item list, which
	// silently returns a listing where the user asked for full text.
	//
	// STE has no typographic emphasis, so salience has to come from
	// position: state the prohibition as a command BEFORE the detail it
	// governs, and name the task that selects the mode before explaining
	// the mode. See scripts/eval/ to re-measure after editing this.
	"To read the full text of one event, use expand mode. Set expand, and do not set limit or cursor. If you set expand together with a limit or a cursor that is not 0, the tool refuses the call. The tool does not silently make the call more narrow.\n\n" +
	"Expand mode occurs when expand is not 0. The mode reads the full text of one event, for example all the findings of a sub-agent. Give an #n from a list that used the same filters. A negative value counts from the end, and -1 is the most recent match. For example, event_kinds \"message\" with expand -1 gives the most recent message in full, and you do not need a list first. If the text is long, use text_offset to read more of it.\n\n" +
	"The default for session_id is the current session. You can give another id from this project, which is a file name without .jsonl, as terva_status shows. You can also give the id of a swarm sub-agent, as swarm_spawn and the [auto-swarm update] message show. To examine a transcript file instead, give path. Use this for a transcript from another machine, or for a transcript that the user gives to you. The path can be any .jsonl file that you can open with the read tool.\n\n" +
	"Do not give path together with session_id, because they are mutually exclusive. The tool removes secrets and limits the size of the output."

func (t *SessionInspectTool) Description() string {
	return i18n.D("tool.session_inspect.description", sessionInspectDesc)
}

func (t *SessionInspectTool) Schema() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id":    map[string]any{"type": "string", "description": "The session to examine. Give a file name without .jsonl, or the id of a swarm sub-agent from this project. Omit this field for the current session."},
			"path":          map[string]any{"type": "string", "description": "The path to a session .jsonl file that you can read with the read tool, for example a transcript from another machine. Do not give this field with session_id, because they are mutually exclusive. You cannot read the sessions of another project in $TERVA_HOME."},
			"failures_only": map[string]any{"type": "boolean", "description": "Show the tool results that failed only."},
			"tool_name":     map[string]any{"type": "string", "description": "Show the events for this tool only."},
			"event_kinds": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"tool_call", "tool_result", "message", "usage", "error"}},
				"description": "Show these event kinds only. The kind \"usage\" gives the cost and the cache accounting of a turn. Use it alone to see where a session spent money. The kind \"error\" is a provider failure such as an authentication error, an overload, or a rate limit. The tool puts each error against the turn that it stopped.",
			},
			"limit":       map[string]any{"type": "integer", "description": "The maximum number of events. The default is 40, and the maximum is 200."},
			"cursor":      map[string]any{"type": "integer", "description": "For list mode only. A position in the events that agree with the filters, where the oldest event is position 1. A value of 0, or no value, gives the most recent window. Do not give this field with an expand value that is not 0, because the tool refuses such a call."},
			"expand":      map[string]any{"type": "integer", "description": "For expand mode. One event to read in full. Give an #n from a list that used the same filters, where the first event is 1. A negative value counts from the end, and -1 is the most recent match. A value of 0, or no value, selects list mode. Do not give this field with a limit or a cursor that is not 0, because the tool refuses such a call."},
			"text_offset": map[string]any{"type": "integer", "description": "For expand mode. The offset in bytes into the text of the event. The default is 0. To continue, use the offset from the notice that the tool sent with the previous part."},
			"stats":       map[string]any{"type": "boolean", "description": "For stats mode. The tool returns a summary of the full session instead of events: the message counts, the period, the cost, the cache hit rate, the dead turns, the counts of tool calls and failures, and the provider errors. The tool ignores the other filters, because the numbers always describe the full session. Start here when you want to know what occurred or what the session cost, and not to see one event."},
		},
		"additionalProperties": false,
	})
	return b
}

func (t *SessionInspectTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a sessionInspectArgs
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
		}
	}
	if a.Expand < -siExpandTailMax {
		return toolErr(fmt.Sprintf("session_inspect: expand %d is too far back — negative expand reaches at most -%d; use the absolute #n from a listing (cursor pages older events)", a.Expand, siExpandTailMax)), nil
	}
	// EXPAND MODE reads ONE event in full and ignores the listing paging fields,
	// so a call naming a real expand target AND a real cursor/limit is
	// contradictory — the caller put one mode's field into the other. Reject it
	// with both corrected forms rather than silently dropping the listing intent.
	//
	// This no longer fires on a padded call: with 0 meaning "unset" on both
	// fields, {"cursor":0,"expand":0,"limit":0} is now simply the default
	// listing. It fires only when both fields carry a value the caller had to
	// choose, which is the case worth explaining.
	if a.Expand != 0 && (a.Cursor != 0 || a.Limit != 0) {
		return toolErr("session_inspect: EXPAND MODE (expand set) reads one event in full and ignores cursor/limit — do not combine them:\n" +
			"  LIST MODE:   leave expand at 0 — e.g. {\"event_kinds\":[\"tool_result\"],\"cursor\":1,\"limit\":40}\n" +
			"  EXPAND MODE: leave cursor/limit at 0 — e.g. {\"event_kinds\":[\"tool_result\"],\"expand\":1}"), nil
	}
	// stats is whole-session by construction, so pairing it with an expand
	// target is a contradiction rather than a narrowing. The listing filters
	// are NOT an error here — a padded call carrying them is the common shape,
	// and stats simply ignores them (said so in the schema).
	if a.Stats && a.Expand != 0 {
		return toolErr("session_inspect: STATS MODE (stats true) summarises the whole session and EXPAND MODE reads one event — pick one:\n" +
			"  STATS:  {\"stats\":true}\n" +
			"  EXPAND: {\"expand\":1}"), nil
	}
	// session_id and path both name a transcript, by different means. A call
	// carrying both has contradicted itself, and picking one silently would send
	// back an analysis of a file the caller did not ask about — the worst
	// outcome, since nothing in the output would reveal the substitution.
	if a.SessionID != "" && a.Path != "" {
		return toolErr("session_inspect: session_id and path both name a transcript — pass one:\n" +
			"  BY ID:   {\"session_id\":\"20260729-184207-2f58d72f\"}  (a session of this project, or a swarm sub-agent)\n" +
			"  BY PATH: {\"path\":\"~/Downloads/20260729-184207-2f58d72f.jsonl\"}  (any transcript you can read)"), nil
	}

	var (
		path       string
		sessID     string
		swarmChild bool
		err        error
	)
	if a.Path != "" {
		path, sessID, err = t.resolveFromPath(a.Path)
	} else {
		path, sessID, swarmChild, err = t.resolvePath(ctx, a.SessionID)
	}
	if err != nil {
		return toolErr("session_inspect: " + err.Error()), nil
	}

	// Authorize a swarm child's project ownership BEFORE scanning its payload. A
	// swarm child's transcript lives under the shared swarm root, not the project
	// sessions dir, so the sessions-dir jail can't confine it — and a known
	// cross-project child id must not be able to impose transcript-parsing work
	// before rejection.
	//
	// Ownership comes from the SPAWN RECORD, not from the child's cwd. Those are
	// the same directory only when the child shares the host's tree; under
	// --swarm-worktrees every child is leased its own worktree, whose path hashes
	// to a different project bucket than its parent's. Reading the cwd therefore
	// rejected every leased child of the very project asking — the flag that
	// makes sub-agents safe silently disabled the tool that watches them, and it
	// survived because an unleased child's Dir IS its parent's RepoRoot, which is
	// the only case the tests covered.
	//
	// Fails closed: an unknown agent yields an empty origin, which matches no
	// project.
	if swarmChild {
		if !swarmChildFromProject(t.TervaHome, t.CWD, swarm.AgentOrigin(swarm.DefaultRoot(t.TervaHome), sessID)) {
			return toolErr(fmt.Sprintf("session_inspect: swarm sub-agent %q was not spawned from this project", sessID)), nil
		}
	}

	// The error sidecar is a separate file and tiny by nature (one short line
	// per provider failure). Read it whole up front so its rows can interleave
	// into the event stream by timestamp — a turn that died on an overload
	// leaves the transcript merely quiet, and this is the only record of why.
	sideErrs, _ := core.ReadSessionErrors(path)

	if a.Stats {
		return sessionStats(ctx, path, sessID, sideErrs)
	}

	scan := newSessScan(a)
	scan.pending = sideErrs
	// One bounded streaming pass: the transcript is never hydrated whole.
	// Retained state is the match count, at most one page of snippets (or the
	// single expand target's text), and the bounded call-id correlations.
	_, scanTrunc, err := streamReplay(ctx, path, siScanCeiling, scan.addRow)
	if err != nil {
		if ctx.Err() != nil {
			return core.ToolResult{}, ctx.Err() // propagate cancellation, don't bury it
		}
		return toolErr("session_inspect: could not read the session transcript"), nil
	}
	// Anything timestamped after the last message lands at the end.
	scan.drainErrors(time.Time{})

	total := scan.total
	// An empty transcript on a swarm child is a timing state, not a filter
	// miss, and reporting it as one sends the caller off re-filtering — which
	// cannot fix it. Since the child now streams its transcript as it works
	// (swarm_agent.go wires WireHeadlessSessionPersist), this window is short:
	// it means the child has not produced its first message yet, or died
	// before it could. Say which, rather than blaming the filters.
	if swarmChild && scan.seen == 0 {
		return toolErr(emptyChildTranscriptMsg(t.TervaHome, sessID)), nil
	}
	if a.Expand != 0 {
		if total == 0 {
			return toolErr("session_inspect: no events match these filters, nothing to expand"), nil
		}
		ev, idx := scan.resolveExpand()
		if ev == nil {
			return toolErr(fmt.Sprintf("session_inspect: expand %d is out of range — %d event(s) match these filters (#1–#%d, or -1 back to -%d)", a.Expand, total, total, total)), nil
		}
		return expandSessionEvent(sessID, *ev, idx, a.TextOffset), nil
	}

	window, start, end := scan.window()
	body := renderSessionEvents(sessID, window, total, start, end, scan.limit)
	if scanTrunc {
		body += scanCeilingNotice
	}
	details := map[string]any{"session_id": sessID, "total": total, "start": wireIndex(start), "count": end - start}
	if end < total {
		details["next_cursor"] = wireIndex(end)
	}
	if scanTrunc {
		details["scan_truncated"] = true
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: body}},
		Details: details,
	}, nil
}

// sessionStats renders a whole-session rollup: what the session did, what
// failed, and what it cost. It is the cheap direction — a fixed-size summary
// over an arbitrarily long transcript — and it exists because the alternative
// is not "page through the events", it is "give up".
//
// Reaching these numbers by listing meant walking hundreds of events at a
// retrieval cost proportional to the whole context, which in a large session
// runs to dollars per pass. That is the same trap that made a truncated swarm
// recap expensive: an answer that is technically reachable but priced so the
// caller never asks. Everything here is derived in ONE streaming pass.
//
// Deliberately ignores the listing filters. A half-filtered rollup invites
// exactly the misreading a rollup is for — "the session spent $3" when that was
// one tool's share — so the numbers always describe the whole session.
func sessionStats(ctx context.Context, path, sessID string, sideErrs []core.SessionError) (core.ToolResult, error) {
	var (
		tools      = map[string]int{}
		failures   = map[string]int{}
		roles      = map[string]int{}
		callName   = map[string]string{}
		usage      provider.Usage
		turns      int
		deadTurns  int
		compaction int
		first      time.Time
		last       time.Time
		// Sub-agent spend booked against this session, kept out of the rollup
		// above so the cache-hit rate describes THIS session's requests.
		delegated      provider.Usage
		delegatedTurns int
	)
	_, truncated, err := streamReplay(ctx, path, siScanCeiling, func(_ int, r core.ReplayRow) {
		switch r.Kind {
		case core.ReplayRowUsage:
			// A sub-agent's spend is not a turn of THIS session, and mixing it
			// in wrecks the one number here worth acting on: a fresh child's
			// prompt is transcript-sized with nothing cached, so it reads as
			// this session's cache collapsing. Counted, named, kept apart.
			if r.Delegated {
				delegatedTurns++
				delegated = delegated.Add(r.Usage)
				return
			}
			turns++
			usage = usage.Add(r.Usage)
			// A turn that recorded no tokens AND no cost never reached the
			// model: the request died before it was billed. In the reviewed
			// session each of these was a turn the user had to re-drive by hand.
			if r.Usage.InputTokens == 0 && r.Usage.OutputTokens == 0 && r.Usage.CostUSD == 0 {
				deadTurns++
			}
		case core.ReplayRowCompaction:
			compaction++
		case core.ReplayRowMessage:
			m := r.Message
			roles[string(m.Role)]++
			if !m.Time.IsZero() {
				if first.IsZero() {
					first = m.Time
				}
				last = m.Time
			}
			for _, c := range m.Content {
				switch b := c.(type) {
				case provider.ToolCallBlock:
					tools[b.Name]++
					if b.ID != "" && len(callName) < siMaxOutstandingCalls {
						callName[b.ID] = b.Name
					}
				case provider.ToolResultBlock:
					if !b.IsError {
						continue
					}
					name := callName[b.CallID]
					if name == "" {
						name = "(unknown)"
					}
					failures[name]++
					delete(callName, b.CallID)
				}
			}
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			return core.ToolResult{}, ctx.Err()
		}
		return toolErr("session_inspect: could not read the session transcript"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "session %s — summary\n\n", sessID)

	fmt.Fprintf(&b, "messages: %d user, %d assistant, %d tool\n", roles["user"], roles["assistant"], roles["tool"])
	if !first.IsZero() && last.After(first) {
		fmt.Fprintf(&b, "span: %s → %s (%s)\n",
			first.Format("2006-01-02 15:04:05"), last.Format("15:04:05"), last.Sub(first).Round(time.Second))
	}
	if compaction > 0 {
		fmt.Fprintf(&b, "compactions: %d\n", compaction)
	}

	// A model with no published rate prices at zero, so a real session reports
	// $0.0000. Printing that as a dollar figure over "billed turns" says the
	// session was free; one reviewed session moved 112 million tokens and read
	// as costing nothing. Absence of a price is not a measurement of spend, and
	// this tool exists so an agent can reason about its own economics — so name
	// the distinction rather than rendering it as zero.
	//
	// Subscriptions used to land here and no longer do: every subscription
	// provider now carries its models' published rates, so the readout is an
	// ESTIMATE of what the tokens would have cost rather than a zero (see
	// "a subscription still has a price, it just isn't yours"). What is left is
	// a model with no rate at all — a local endpoint, an uncatalogued model —
	// and github-copilot, which bills per premium request rather than per token.
	// The wording follows: saying "subscription" here would now name the two
	// cases that are no longer it.
	if usage.CostUSD == 0 && usage.InputTokens+usage.CacheReadTokens+usage.OutputTokens > 0 {
		fmt.Fprintf(&b, "\ncost: not priced — no published rate for this model, over %d turn(s)\n", turns)
	} else {
		fmt.Fprintf(&b, "\ncost: $%.4f over %d billed turn(s)\n", usage.CostUSD+delegated.CostUSD, turns)
	}
	// Cache writes are named here because they are in the denominator below —
	// a rollup that omitted them printed three numbers that did not add up to
	// the total the rate was taken over.
	fmt.Fprintf(&b, "  uncached input %d, cache reads %d, cache writes %d, output %d\n",
		usage.InputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.OutputTokens)
	if rate, ok := usage.CacheHitRate(); ok {
		// The single most actionable number here. A low rate on a long session
		// means the context is being re-sent at full price, turn after turn.
		// Computed over this session's OWN requests: a sub-agent starts cold by
		// definition, so counting its prompts here would report delegation as a
		// caching failure.
		//
		// Through CacheHitRate so this and the /context pane cannot print two
		// different percentages for one session under the same label — they
		// did, because this took the rate over Input+CacheRead and the pane
		// took it over the whole prompt.
		fmt.Fprintf(&b, "  cache hit rate %.1f%% of prompt\n", rate*100)
	}
	if delegatedTurns > 0 {
		// Inside the cost above, not added to it — the same subset relationship
		// terva_status draws, and for the same reason: a coordinator can spend
		// an order of magnitude more through sub-agents than on its own turns.
		fmt.Fprintf(&b, "  of which delegated to sub-agents: $%.4f over %d record(s), %d in / %d out (excluded from the hit rate above)\n",
			delegated.CostUSD, delegatedTurns,
			delegated.PromptTokens(), delegated.OutputTokens)
	}
	if deadTurns > 0 {
		fmt.Fprintf(&b, "  %d turn(s) recorded zero tokens and zero cost — they died before reaching the model\n", deadTurns)
	}

	if len(tools) > 0 {
		b.WriteString("\ntool calls:\n")
		writeCountTable(&b, tools, siStatsTopN)
	}
	if len(failures) > 0 {
		b.WriteString("\nfailed tool results:\n")
		writeCountTable(&b, failures, siStatsTopN)
	}

	if len(sideErrs) > 0 {
		fmt.Fprintf(&b, "\nprovider errors (%d, from the error sidecar):\n", len(sideErrs))
		byText := map[string]int{}
		for _, e := range sideErrs {
			byText[eventSnippet(labelSessionError(e))]++
		}
		writeCountTable(&b, byText, siStatsTopN)
		b.WriteString("  (event_kinds [\"error\"] lists these in place, against the turns they killed)\n")
	}

	if truncated {
		b.WriteString("\n" + scanCeilingNotice)
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: b.String()}},
		Details: map[string]any{
			"session_id": sessID, "cost_usd": usage.CostUSD, "turns": turns,
			"input_tokens": usage.InputTokens, "cache_read_tokens": usage.CacheReadTokens,
			"output_tokens": usage.OutputTokens, "dead_turns": deadTurns,
			"provider_errors": len(sideErrs),
		},
	}, nil
}

// labelSessionError renders a sidecar row as one line. The provider is
// prepended only when the message does not already carry it: the error text is
// stamped by the client, which usually prefixes its own name, and blindly
// prepending produced "openai-codex: openai-codex: …".
func labelSessionError(e core.SessionError) string {
	text := e.Error
	if e.Provider != "" && !strings.HasPrefix(text, e.Provider+":") {
		text = e.Provider + ": " + text
	}
	if e.Model != "" {
		text += "  [" + e.Model + "]"
	}
	return text
}

// siStatsTopN bounds each rollup table so a session with a long tail of
// one-off tool names cannot push the summary past a readable size.
const siStatsTopN = 25

// writeCountTable renders a name→count map, largest first, ties broken by name
// so repeated calls on one transcript render identically.
func writeCountTable(b *strings.Builder, counts map[string]int, top int) {
	type row struct {
		name string
		n    int
	}
	rows := make([]row, 0, len(counts))
	for k, v := range counts {
		rows = append(rows, row{k, v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].name < rows[j].name
	})
	shown := rows
	if top > 0 && len(shown) > top {
		shown = shown[:top]
	}
	for _, r := range shown {
		fmt.Fprintf(b, "  %5d  %s\n", r.n, r.name)
	}
	if len(rows) > len(shown) {
		fmt.Fprintf(b, "  …and %d more\n", len(rows)-len(shown))
	}
}

// siScanCeiling caps the transcript bytes one call will scan — a guard against
// a pathological multi-hundred-MiB file, far above any real session. A var so
// tests can lower it without writing a giant fixture.
var siScanCeiling int64 = 64 << 20

// emptyChildTranscriptMsg explains a swarm child whose transcript has nothing
// in it yet. The child's event log is the discriminator: it streams from the
// first thing the child does, so a non-empty log with an empty transcript means
// the child is alive and has simply not finished a message, while an empty log
// means it never got that far.
//
// The recap promise is deliberately conditioned on swarm_spawn rather than
// stated flat: trackSwarmAgent is wired through SwarmSpawnTool.OnSpawned only
// when auto-swarm is on, so a child started from the TUI's /swarm gets no
// [auto-swarm update]. Telling that caller to sit and wait for a push that
// never arrives would trade one dead end for a worse one.
func emptyChildTranscriptMsg(tervaHome, id string) string {
	working := false
	if tervaHome != "" {
		if fi, err := os.Stat(swarm.AgentEventLogPath(swarm.DefaultRoot(tervaHome), id)); err == nil && fi.Size() > 0 {
			working = true
		}
	}
	state := "has not started writing a transcript — it may have failed before its first turn"
	if working {
		state = "is running but has not completed its first message yet"
	}
	return fmt.Sprintf("session_inspect: sub-agent %q %s. A sub-agent streams its transcript as it works, so this is a timing state, not a filter miss — no combination of event_kinds, limit, or cursor changes it, and inspecting again shortly will show whatever it has produced by then. If you spawned it with swarm_spawn you do not need to watch it at all: its findings are pushed to you as an [auto-swarm update] when the task ends.", id, state)
}

// swarmChildFromProject reports whether a sub-agent spawned at origin belongs to
// the project rooted at cwd. Shared by session_inspect's authorization gate and
// session_search's enumeration filter so the two cannot drift — a second copy of
// this predicate is precisely the twin this repo keeps finding.
//
// Ownership comes from the SPAWN RECORD, not the child's cwd: under
// --swarm-worktrees a child is leased its own worktree, which hashes to a
// different project bucket than its parent's, so comparing cwds rejects every
// leased child of the very project asking.
//
// Fails closed — an empty origin (unknown or malformed agent) matches nothing.
func swarmChildFromProject(tervaHome, cwd, origin string) bool {
	if origin == "" {
		return false
	}
	return core.SessionsDir(tervaHome, origin) == core.SessionsDir(tervaHome, cwd)
}

// streamReplay is core.StreamReplayRows behind a package var so a test can
// assert the swarm-child project-authorization gate runs BEFORE any transcript
// scan (the cross-project parsing-DoS the reorder closes).
var streamReplay = core.StreamReplayRows

const scanCeilingNotice = "note: part of the transcript was skipped — an oversized row, or the 64 MiB scan ceiling was reached; totals and windows may not cover the whole file (some events may be missing)\n"

// siMaxOutstandingCalls bounds the tool-call→name correlations retained during a
// scan. In a healthy transcript a call's result lands in the very next message,
// so at most one turn's parallel calls are ever outstanding — orders of
// magnitude under this cap, which is therefore never reached and never evicts.
// Eviction only ever fires on a transcript with >1024 calls outstanding at once
// (damaged input), where a dropped correlation merely leaves that one result's
// tool name blank — it never misattributes a name.
const siMaxOutstandingCalls = 1024

// siExpandTailMax is the deepest negative expand index (-1 = most recent
// match). It matches the listing page cap, and bounds the full-text tail ring
// a negative expand retains during the scan.
const siExpandTailMax = 200

// sessScan accumulates the bounded state one streaming pass retains. Exactly
// one of the retention modes is active: the latest-N ring / an exact cursor
// page (listing), the single expand target's full text, or — for a negative
// expand, whose target is unknown until the matches are counted — a full-text
// ring of the last |expand| matches.
type sessScan struct {
	kinds    map[string]bool
	tool     string
	failOnly bool

	// callName correlates a tool_result back to its call's tool name. Entries are
	// consumed as results match them, so in a well-formed transcript it holds only
	// the current turn's outstanding calls. A DAMAGED transcript (many calls, no
	// results) would otherwise grow it with the event count, so it is capped at
	// siMaxOutstandingCalls with oldest-first eviction (a bounded recent window).
	// callOrder tracks insertion order for eviction and is compacted of consumed
	// ids so it, too, stays O(cap), not O(call count).
	callName  map[string]string
	callOrder []string

	// seen counts every flattened event BEFORE the filters, so an empty
	// result can be attributed: seen>0 with total==0 means the filters
	// excluded everything, while seen==0 means the transcript itself carried
	// nothing. Only the latter says anything about the session's state.
	seen   int
	total  int
	limit  int
	cursor *int
	expand *int

	ring       []sessEvent // page/ring storage; nil in expand mode
	expandEv   *sessEvent
	expandRing []sessEvent // tail ring for a negative expand; nil otherwise

	// pending holds sidecar errors not yet placed into the stream, oldest
	// first. Drained by timestamp as messages pass (see drainErrors).
	pending []core.SessionError

	// lastUsageAt is the previous stamped usage row's time, for the gap between
	// consecutive dispatches. Zero until the first stamped row, and never
	// advanced by an unstamped one — a gap measured across a legacy row would
	// span an unknown number of requests and read as a long idle that never
	// happened.
	lastUsageAt time.Time
}

func newSessScan(a sessionInspectArgs) *sessScan {
	limit := a.Limit
	if limit <= 0 {
		limit = 40
	}
	if limit > 200 {
		limit = 200
	}
	s := &sessScan{
		tool:     strings.TrimSpace(a.ToolName),
		failOnly: a.FailuresOnly,
		callName: map[string]string{},
		limit:    limit,
		cursor:   cursorOffset(a.Cursor),
		expand:   expandTarget(a.Expand),
	}
	if len(a.EventKinds) > 0 {
		s.kinds = make(map[string]bool, len(a.EventKinds))
		for _, k := range a.EventKinds {
			s.kinds[k] = true
		}
	}
	return s
}

// noteCall records a tool_call id→name, capped at siMaxOutstandingCalls with
// oldest-first eviction. callOrder is compacted of consumed ids once it carries
// far more than the live set, so retained state is O(cap), not O(call count).
func (s *sessScan) noteCall(id, name string) {
	if id == "" {
		return
	}
	if _, ok := s.callName[id]; ok {
		s.callName[id] = name
		return
	}
	if len(s.callName) >= siMaxOutstandingCalls {
		for len(s.callOrder) > 0 { // evict the oldest still-live id
			oldest := s.callOrder[0]
			s.callOrder = s.callOrder[1:]
			if _, live := s.callName[oldest]; live {
				delete(s.callName, oldest)
				break
			}
		}
	}
	s.callName[id] = name
	s.callOrder = append(s.callOrder, id)
	if len(s.callOrder) > 2*siMaxOutstandingCalls { // drop consumed tombstones, keep order
		live := s.callOrder[:0]
		for _, cid := range s.callOrder {
			if _, ok := s.callName[cid]; ok {
				live = append(live, cid)
			}
		}
		s.callOrder = live
	}
}

// takeCall returns and consumes the recorded name for a result's call id, or ""
// when the call was never seen or its correlation was evicted.
func (s *sessScan) takeCall(id string) string {
	name := s.callName[id]
	delete(s.callName, id)
	return name
}

// addRow dispatches one streamed replay row into the scan. Usage rows carry no
// text, so they bypass the message flattening entirely.
func (s *sessScan) addRow(row int, r core.ReplayRow) {
	switch r.Kind {
	case core.ReplayRowMessage:
		// Sidecar errors are timestamped, not row-numbered, so they are placed
		// just before the first message that postdates them. That puts an
		// overload immediately after the turn it killed, which is the only
		// position from which it explains anything.
		s.drainErrors(r.Message.Time)
		s.addMessage(row, r.Message)
	case core.ReplayRowUsage:
		// A usage row is the closest event to the request a sidecar error
		// killed — closer than the next message, which may be many tool calls
		// later. Guarded on the zero time because drainErrors treats a zero
		// cutoff as "drain everything", so an unstamped legacy row would empty
		// the queue here and hang every later error off the wrong turn.
		if !r.At.IsZero() {
			s.drainErrors(r.At)
		}
		s.addUsage(row, r.Usage, r.Cumulative, r.At)
	}
}

// drainErrors emits every pending sidecar error at or before cutoff. A zero
// cutoff drains the rest, for the errors that postdate the last message.
func (s *sessScan) drainErrors(cutoff time.Time) {
	for len(s.pending) > 0 {
		e := s.pending[0]
		if !cutoff.IsZero() && e.Time.After(cutoff) {
			return
		}
		s.pending = s.pending[1:]
		// Row -1: a sidecar row has no transcript row, and inventing one would
		// collide with a real event's stable reference. Rendered as "-".
		s.add(sessEvent{Row: -1, Kind: "error", IsError: true, Bytes: len(e.Error), Text: labelSessionError(e)})
	}
}

// addUsage flattens one usage row into an event. What a turn cost — and how
// much of its input hit the prefix cache — is not derivable from the messages,
// so without this the tool can describe everything a session did except what it
// spent doing it.
func (s *sessScan) addUsage(row int, u, cum provider.Usage, at time.Time) {
	ev := &usageEvent{Usage: u, Cumulative: cum, At: at}
	if !at.IsZero() {
		if !s.lastUsageAt.IsZero() {
			ev.Since = at.Sub(s.lastUsageAt)
		}
		s.lastUsageAt = at
	}
	s.add(sessEvent{Row: row, Kind: "usage", Usage: ev})
}

// addMessage flattens one transcript message into per-block events, feeding
// each through the filters into the bounded retention. The signature matches
// core.StreamReplayMessages' callback.
func (s *sessScan) addMessage(row int, m provider.Message) {
	role := string(m.Role)
	for _, c := range m.Content {
		switch b := c.(type) {
		case provider.ToolCallBlock:
			s.noteCall(b.ID, b.Name)
			s.add(sessEvent{Row: row, Kind: "tool_call", Role: role, Tool: b.Name, Bytes: len(b.Arguments), Text: string(b.Arguments)})
		case provider.ToolResultBlock:
			name := s.takeCall(b.CallID)
			txt := flattenResultText(b.Content)
			s.add(sessEvent{Row: row, Kind: "tool_result", Role: role, Tool: name, IsError: b.IsError, Bytes: len(txt), Text: txt})
		case provider.TextBlock:
			if strings.TrimSpace(b.Text) != "" {
				s.add(sessEvent{Row: row, Kind: "message", Role: role, Bytes: len(b.Text), Text: b.Text})
			}
		}
	}
}

// add applies the filters and retains the event only if the active mode needs
// it: the expand target keeps its full text; a listed event keeps only its
// snippet source. Everything else contributes to the count and is dropped.
func (s *sessScan) add(e sessEvent) {
	s.seen++
	if s.kinds != nil && !s.kinds[e.Kind] {
		return
	}
	if s.tool != "" && e.Tool != s.tool {
		return
	}
	if s.failOnly && !(e.Kind == "tool_result" && e.IsError) {
		return
	}
	idx := s.total
	s.total++

	if s.expand != nil {
		if *s.expand >= 0 {
			if idx == *s.expand {
				ev := e
				s.expandEv = &ev
			}
			return
		}
		// Negative index: the target is only known once the matches are
		// counted, so keep the last |expand| matches at full length and let
		// resolveExpand pick one after the scan.
		size := -*s.expand
		if len(s.expandRing) < size {
			s.expandRing = append(s.expandRing, e)
		} else {
			s.expandRing[idx%size] = e
		}
		return
	}

	e.Text = clipSnippetSource(e.Text)
	if s.cursor == nil {
		// Latest-N ring: match idx lands at idx%limit, so the final window
		// [total-len, total) reads back in order without shifting.
		if len(s.ring) < s.limit {
			s.ring = append(s.ring, e)
		} else {
			s.ring[idx%s.limit] = e
		}
		return
	}
	if start := max(*s.cursor, 0); idx >= start && idx < start+s.limit {
		s.ring = append(s.ring, e)
	}
}

// resolveExpand maps the requested expand index to the retained event and its
// absolute #n coordinate, resolving a negative index against the final match
// count (-1 = most recent). Nil when the index falls outside [0, total). The
// absolute coordinate is what the rendered event and its paging hints carry:
// -1 can name a different event once the session grows, #n cannot.
func (s *sessScan) resolveExpand() (*sessEvent, int) {
	idx := *s.expand
	if idx < 0 {
		idx += s.total
	}
	if idx < 0 || idx >= s.total {
		return nil, idx
	}
	if *s.expand >= 0 {
		return s.expandEv, idx
	}
	return &s.expandRing[idx%(-*s.expand)], idx
}

// window assembles the render slice and its [start, end) coordinates from the
// retained page/ring.
func (s *sessScan) window() (events []sessEvent, start, end int) {
	if s.cursor == nil {
		start = s.total - len(s.ring)
		for p := start; p < s.total; p++ {
			events = append(events, s.ring[p%s.limit])
		}
		return events, start, s.total
	}
	start = max(*s.cursor, 0)
	if start > s.total {
		start = s.total
	}
	return s.ring, start, start + len(s.ring)
}

// siSnippetSourceMax bounds the text a listed event retains during the scan.
// It is deliberately larger than the rendered snippet (siSnippetMax) so
// render-time redaction sees well past the display cut — a secret spanning
// the 200-byte boundary still falls inside the redacted window.
const siSnippetSourceMax = 512

func clipSnippetSource(s string) string {
	if len(s) > siSnippetSourceMax {
		return s[:siSnippetSourceMax]
	}
	return s
}

// resolveFromPath admits a session-shaped JSONL by filesystem path, bounded by
// the SAME read policy the read tool applies.
//
// That equivalence is the whole security argument, and it is worth stating
// plainly because "let a tool open an arbitrary path" reads like a hole. It is
// not one here: every byte reachable this way is already reachable with `read`
// or `cat`. What the model gains is a LENS, not access — the structured,
// redacted, bounded view of a transcript it could otherwise only page through as
// raw JSONL, which is precisely what defeated two harness reviews (both were
// handed a session file from another machine and had to analyze it with an
// out-of-tree script).
//
// The converse holds too, and is what keeps the project scoping intact:
// $TERVA_HOME/sessions and swarm/ are registered secret roots, so a path
// pointing into ANOTHER project's transcripts is refused here exactly as it is
// for read — with the deny list's own message, which already says that bash
// cannot reach it either and /unjail does not lift it. Cross-project inspection
// therefore remains closed, and closes with an explanation rather than a
// puzzling "no such session".
func (t *SessionInspectTool) resolveFromPath(raw string) (path, id string, err error) {
	p := resolvePath(t.CWD, raw)
	if err := t.Sandbox.CheckPathRead(p); err != nil {
		return "", "", err
	}
	info, serr := os.Stat(p)
	if serr != nil {
		// A missing transcript is the ordinary case, and it gets the shared
		// diagnostic. Anything else keeps its framing, because the operating
		// system knows something the caller does not.
		if os.IsNotExist(serr) {
			return "", "", notFoundError(t.CWD, raw, t.Sandbox.DisplayPath(p, raw), serr)
		}
		return "", "", fmt.Errorf("cannot read %q: %w", t.Sandbox.DisplayPath(p, raw), serr)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("%s is a directory, not a session transcript", t.Sandbox.DisplayPath(p, raw))
	}
	// The id is only a label on the output. Derived from the filename so a
	// downloaded transcript still reports the id it was recorded under, which is
	// what a reader correlates against everything else about that session.
	return p, core.SessionIDFromPath(p), nil
}

// resolvePath maps a session_id (or the current session) to a transcript path,
// refusing any id that escapes the active project's sessions directory. An id
// that is not a project session but names a swarm sub-agent resolves to that
// child's transcript under the swarm root (swarmChild=true); the caller must
// then enforce the project confinement the sessions-dir jail provides here,
// against the transcript's own recorded cwd.
func (t *SessionInspectTool) resolvePath(ctx context.Context, sessionID string) (path, id string, swarmChild bool, err error) {
	if sessionID != "" {
		if strings.ContainsAny(sessionID, `/\`) || strings.Contains(sessionID, "..") {
			return "", "", false, fmt.Errorf("invalid session_id")
		}
		p := filepath.Join(core.SessionsDir(t.TervaHome, t.CWD), sessionID+".jsonl")
		if _, e := os.Stat(p); e == nil {
			return p, sessionID, false, nil
		}
		if t.TervaHome != "" {
			sp := swarm.AgentSessionPath(swarm.DefaultRoot(t.TervaHome), sessionID)
			if _, e := os.Stat(sp); e == nil {
				return sp, sessionID, true, nil
			}
		}
		return "", "", false, fmt.Errorf("no such session or swarm sub-agent %q in this project — session ids are transcript filenames without .jsonl (terva_status prints the current one); sub-agent ids are what swarm_spawn and the [auto-swarm update] recap print", sessionID)
	}
	ag := core.AgentFromContext(ctx)
	if ag == nil {
		ag = t.Agent
	}
	if ag == nil {
		return "", "", false, fmt.Errorf("no active session to inspect")
	}
	sid, p := ag.SessionIdentity()
	if p == "" {
		return "", "", false, fmt.Errorf("this session has no transcript file (e.g. running with --no-session)")
	}
	return p, sid, false, nil
}

// sessEvent is one flattened, inspectable transcript event.
type sessEvent struct {
	Row     int    // source transcript row (a stable reference, not the cursor)
	Kind    string // tool_call | tool_result | message | usage
	Role    string
	Tool    string // tool name (a call's own, or a result's via call_id correlation)
	IsError bool
	Bytes   int
	Text    string

	// Usage is set when Kind == "usage" — the only event kind whose payload is
	// numbers rather than text, so it renders from these fields instead of Text
	// and is unaffected by the snippet/expand byte budgets.
	Usage *usageEvent
}

// usageEvent is a turn's recorded cost alongside the session's running total.
type usageEvent struct {
	Usage      provider.Usage
	Cumulative provider.Usage

	// At is when the request was billed, zero for rows written before usage rows
	// carried a stamp. Since is the gap from the previous stamped dispatch, zero
	// when there is no previous one to measure from.
	//
	// The gap is here because a prefix cache ages out on IDLE time, so "did this
	// miss because the prefix expired" is a question about the interval between
	// requests and nothing else in the transcript records it.
	At    time.Time
	Since time.Duration
}

// line renders a usage event for a listing: the turn's own numbers, then the
// running total. Uncached input and cache reads are kept SEPARATE rather than
// summed into one "input" figure, because the ratio between them is the whole
// signal — the same token count costs an order of magnitude more when it misses
// the prefix cache, and a combined number hides exactly that.
func (u *usageEvent) line() string {
	return fmt.Sprintf("in %d  cached %d  out %d  $%.4f  (session $%.4f)",
		u.Usage.InputTokens, u.Usage.CacheReadTokens, u.Usage.OutputTokens,
		u.Usage.CostUSD, u.Cumulative.CostUSD)
}

// detail renders a usage event for expand mode: every recorded field, one per
// line, including the cache-write and cumulative token counts the one-line form
// leaves out.
func (u *usageEvent) detail() string {
	var b strings.Builder
	if !u.At.IsZero() {
		fmt.Fprintf(&b, "at                   %s", u.At.Format(time.RFC3339))
		if u.Since > 0 {
			fmt.Fprintf(&b, "   (%s after the previous dispatch)", u.Since.Round(time.Second))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "turn:\n")
	fmt.Fprintf(&b, "  input_tokens       %d   (uncached — billed at full rate)\n", u.Usage.InputTokens)
	fmt.Fprintf(&b, "  cache_read_tokens  %d\n", u.Usage.CacheReadTokens)
	fmt.Fprintf(&b, "  cache_write_tokens %d\n", u.Usage.CacheWriteTokens)
	fmt.Fprintf(&b, "  output_tokens      %d\n", u.Usage.OutputTokens)
	fmt.Fprintf(&b, "  cost_usd           %.6f\n", u.Usage.CostUSD)
	// Over the whole prompt, which includes the cache_write_tokens printed four
	// lines above — excluding them meant this rate ignored a number it had just
	// shown the reader.
	if rate, ok := u.Usage.CacheHitRate(); ok {
		fmt.Fprintf(&b, "  cache hit rate     %.1f%% of prompt\n", rate*100)
	}
	fmt.Fprintf(&b, "session cumulative:\n")
	fmt.Fprintf(&b, "  input_tokens       %d\n", u.Cumulative.InputTokens)
	fmt.Fprintf(&b, "  cache_read_tokens  %d\n", u.Cumulative.CacheReadTokens)
	fmt.Fprintf(&b, "  output_tokens      %d\n", u.Cumulative.OutputTokens)
	fmt.Fprintf(&b, "  cost_usd           %.6f\n", u.Cumulative.CostUSD)
	return b.String()
}

func flattenResultText(blocks []provider.Content) string {
	var b strings.Builder
	for _, c := range blocks {
		if tb, ok := c.(provider.TextBlock); ok && tb.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

const (
	siSnippetMax = 200   // bytes of event text shown per line
	siOutputMax  = 8192  // total rendered body cap (listing mode)
	siExpandMax  = 16384 // bytes of one event's text per expand call
)

func renderSessionEvents(sessID string, window []sessEvent, total, start, end, limit int) string {
	var b strings.Builder
	// The header counted from 1 while the #n lines counted from 0, so "showing
	// 1–5" listed #0–#4. Both are 1-based now, and a caller reading a coordinate
	// off a line gets the one the header promised.
	//
	// Zero matches has no range to show: the old form rendered "showing 1–0",
	// which reads as a bug in the tool rather than as an empty result.
	if total == 0 {
		fmt.Fprintf(&b, "session %s — no events match these filters\n", sessID)
		return b.String()
	}
	fmt.Fprintf(&b, "session %s — %d matching event(s); showing %d–%d\n", sessID, total, wireIndex(start), end)
	for i, e := range window {
		// A usage event has no tool, status, or byte count — forcing it through
		// the text-event columns would print three empty fields and a "0B" that
		// reads as a defect. It shares only the [#n row r] kind prefix, so a
		// caller still reads coordinates off it the same way.
		var line string
		switch {
		case e.Usage != nil:
			line = fmt.Sprintf("[#%d row %d] %-12s %s\n", wireIndex(start+i), e.Row, e.Kind, e.Usage.line())
		case e.Kind == "error":
			// No transcript row and no tool: a sidecar error is placed by
			// timestamp, so "row -" says it has no row rather than showing -1.
			line = fmt.Sprintf("[#%d row -] %-12s %s\n", wireIndex(start+i), e.Kind, eventSnippet(e.Text))
		default:
			line = fmt.Sprintf("[#%d row %d] %-12s %-16s %-4s %6dB  %s\n", wireIndex(start+i), e.Row, e.Kind, e.Tool, eventStatus(e), e.Bytes, eventSnippet(e.Text))
		}
		if b.Len()+len(line) > siOutputMax {
			b.WriteString("…(output truncated; narrow with filters or a smaller limit)\n")
			return b.String()
		}
		b.WriteString(line)
	}
	if end < total {
		fmt.Fprintf(&b, "more: pass cursor %d (with the same filters) for the next %d\n", wireIndex(end), limit)
	}
	if start > 0 {
		// Floors at the OLDEST page, which is wire cursor 1 — not 0, which now
		// means "unset" and would hand back the most recent window instead of
		// the earliest one the caller asked to walk back to.
		earlier := start - limit
		if earlier < 0 {
			earlier = 0
		}
		fmt.Fprintf(&b, "earlier: pass cursor %d\n", wireIndex(earlier))
	}
	return b.String()
}

func eventStatus(e sessEvent) string {
	if e.Kind != "tool_result" {
		return ""
	}
	if e.IsError {
		return "FAIL"
	}
	return "ok"
}

// expandSessionEvent renders ONE matching event's full redacted text with
// newlines preserved — the recovery path for a long deliverable a 200-byte
// snippet can't carry (e.g. a swarm sub-agent's final report). Bounded per
// call at siExpandMax and paged by textOffset, so even a huge event is read
// in a few explicit, size-known steps. idx addresses the same filtered,
// oldest-first coordinate space cursor uses (the #n each listing line shows);
// the caller resolves it to the one retained event (range-checked there).
func expandSessionEvent(sessID string, e sessEvent, idx, textOffset int) core.ToolResult {
	// A usage event's payload is a fixed handful of numbers, so it is always
	// rendered whole: there is nothing for textOffset to page through, and
	// reporting a byte range over it would invite a second call that returns
	// the same thing.
	if e.Usage != nil {
		body := fmt.Sprintf("session %s — event #%d (row %d, usage)\n%s", sessID, wireIndex(idx), e.Row, e.Usage.detail())
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: body}},
			Details: map[string]any{
				"session_id": sessID, "expand": wireIndex(idx),
				"input_tokens": e.Usage.Usage.InputTokens, "cache_read_tokens": e.Usage.Usage.CacheReadTokens,
				"cache_write_tokens": e.Usage.Usage.CacheWriteTokens, "output_tokens": e.Usage.Usage.OutputTokens,
				"cost_usd": e.Usage.Usage.CostUSD, "cumulative_cost_usd": e.Usage.Cumulative.CostUSD,
			},
		}
	}
	text := core.RedactSecrets(e.Text)
	if textOffset < 0 {
		textOffset = 0
	}
	if textOffset > len(text) {
		textOffset = len(text)
	}
	chunk := text[textOffset:]
	truncated := len(chunk) > siExpandMax
	if truncated {
		// Cut on a rune boundary WITHOUT dropping bytes (ToValidUTF8 removes
		// them), so textOffset+len(chunk) stays an exact continuation offset.
		cut := siExpandMax
		for cut > 0 && !utf8.RuneStart(chunk[cut]) {
			cut--
		}
		chunk = chunk[:cut]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "session %s — event #%d (row %d, %s", sessID, wireIndex(idx), e.Row, e.Kind)
	if e.Tool != "" {
		fmt.Fprintf(&b, " %s", e.Tool)
	}
	if s := eventStatus(e); s != "" {
		fmt.Fprintf(&b, " %s", s)
	}
	fmt.Fprintf(&b, "; bytes %d–%d of %d redacted)\n", textOffset, textOffset+len(chunk), len(text))
	b.WriteString(chunk)
	if truncated {
		fmt.Fprintf(&b, "\n…more: pass expand %d with text_offset %d (same filters)\n", wireIndex(idx), textOffset+len(chunk))
	}
	details := map[string]any{"session_id": sessID, "expand": wireIndex(idx), "text_offset": textOffset, "text_total": len(text)}
	if truncated {
		details["next_text_offset"] = textOffset + len(chunk)
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: b.String()}},
		Details: details,
	}
}

// eventSnippet redacts secrets, collapses whitespace to one line, and bounds
// length on a valid UTF-8 boundary.
func eventSnippet(s string) string {
	s = core.RedactSecrets(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > siSnippetMax {
		s = strings.ToValidUTF8(s[:siSnippetMax], "") + "…"
	}
	return s
}
