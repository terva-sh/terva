//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/jsengine"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

const (
	// defaultCodeExecTimeout bounds a script when the model omits an
	// explicit timeout. Scripts are expected to be short lookups; a
	// runaway loop is interrupted by the engine's context watchdog.
	defaultCodeExecTimeout = 30 * time.Second
	maxCodeExecTimeout     = 120 * time.Second
)

// CodeExecutionTool runs a short JavaScript program whose only
// capabilities are a read-only allowlist of the host's own tools, exposed
// as functions. Only print()ed output returns to the model, so multi-step
// lookups cost one tool result instead of N — the "zero-context-cost tool
// orchestration" idea (docs/plans/jsengine-code-execution-and-workflows.md,
// which also fixes the classification rule: this tool is read-only exactly
// as long as every binding below is; any future mutating binding must flip
// its readOnlyTools membership).
type CodeExecutionTool struct {
	// HostCall runs one of the host's own tools through the SAME approval
	// gate a model-issued call uses (reach, not authority — the in-process
	// twin of ext host_tool_call). Late-bound where the live agent and
	// gate exist (build.WireHostToolDispatcher); Execute fails closed when
	// unset.
	HostCall func(ctx context.Context, tool string, args json.RawMessage) (core.ToolResult, error)

	// Catalog is the session's disclosure catalog (§12.7): the tools a
	// script may enumerate and call beyond its fixed bindings. Late-bound
	// beside HostCall, where the agent's ReadOnlySet is in scope. Nil means
	// the tools()/describe()/call() bindings fail closed.
	Catalog *DisclosureCatalog
}

type codeExecArgs struct {
	Script  string `json:"script"`
	Timeout int    `json:"timeout,omitempty"`
}

const codeExecSchema = `{"type":"object","properties":{"script":{"type":"string","description":"A short JavaScript program. You can call read(path[,offset,limit]), grep(pattern[,path]), and glob(pattern[,path]). Each function returns the text output of the tool as a string. Use print(...) to return a result."},"timeout":{"type":"integer","description":"The maximum run time in seconds. The tool then stops the program. The default is 30 seconds, and the maximum is 120 seconds."}},"required":["script"]}`

func (t *CodeExecutionTool) Name() string { return "code_execution" }
func (t *CodeExecutionTool) Description() string {
	return i18n.D("tool.code_execution.description", "Run a short JavaScript program. The program can call read(path[,offset,limit]), grep(pattern[,path]), and glob(pattern[,path]) as functions. Use print(...) to return a result.\n\nThe tool returns the printed output only. The results of the calls in the program do not enter your context. Therefore use this tool for a read-only task with many steps, when a large output gives a small answer. Examples are to count the matches, to extract one field, or to join the results from several files.\n\nThe program has no access to the file system, the network, require, or other globals. Each read, grep, and glob call obeys the usual permission gate. The program can make 50 calls to the host and can print 32KB. The default time limit is 30 seconds.")
}
func (t *CodeExecutionTool) Schema() json.RawMessage { return json.RawMessage(codeExecSchema) }

// ToolGroupName places code_execution in the lazily-activated "scripting"
// group (core.ToolGroup): an optional capability, not a core coding tool,
// so it stays off the default advertised manifest under lazy tools.
func (t *CodeExecutionTool) ToolGroupName() string { return "scripting" }

// scriptReadOnlyBindings is the read-only binding set, in reporting order.
var scriptReadOnlyBindings = []string{"read", "grep", "glob"}

// accountedNames is the binding set the pre-check and the preview account
// for: the fixed bindings, the disclosure verbs, and every tool the catalog
// discloses this session. A catalog name must be in the set, or
// call("session_inspect", …) walks as an unknown identifier and the account
// reports clean while the script reaches a host tool — the failure the
// disclosure exists to rule out.
func (t *CodeExecutionTool) accountedNames() []string {
	names := append([]string(nil), scriptReadOnlyBindings...)
	names = append(names, "tools", "describe", "call")
	names = append(names, t.Catalog.Names()...)
	return names
}

// Preview contributes this tool's confirmation-prompt line via the optional
// accessor core.ToolPreview reads. Where BuildPreview can only show the raw
// script wrapped in JSON, the accounted binding plan is what an approver
// needs. Pure and pre-execution: AnalyzeBindings only parses, HostCall is not
// consulted, and "" falls back to BuildPreview when the analysis cannot run.
// The gate enforces; a preview that cannot be computed changes only what is
// shown, not what runs.
func (t *CodeExecutionTool) Preview(args json.RawMessage, maxLen int) string {
	var a codeExecArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return ""
	}
	refs, aerr := jsengine.AnalyzeBindings("code_execution.js", a.Script, t.accountedNames())
	if aerr != nil {
		return ""
	}
	if refs.Complete {
		return "accounted for: " + bindingPlanList(t.accountedNames(), refs)
	}
	return fmt.Sprintf("unaccountable (%s)", strings.Join(refs.Reasons, "; "))
}

// bindingPlanList renders an accounted analysis as the line a human or a
// transcript reads: "read x5". It is shared by both scripting tools, each
// passing its own binding set.
func bindingPlanList(bindings []string, refs jsengine.BindingRefs) string {
	parts := make([]string, 0, len(bindings))
	for _, name := range bindings {
		if n := refs.Calls[name]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s x%d", name, n))
		}
	}
	if len(parts) == 0 {
		return "no host calls"
	}
	return strings.Join(parts, ", ")
}

// hostCallFn is the gated dispatcher both scripting tools hold: it runs
// one of the host's own tools through the SAME approval gate a
// model-issued call uses.
type hostCallFn func(ctx context.Context, tool string, args json.RawMessage) (core.ToolResult, error)

// dispatchHostTool is the single crossing from a script binding to a host
// tool. Both scripting tools route through it so the gate semantics, the
// error shape, and the flattening of a tool result to script-visible text
// cannot drift apart between a read-only and a mutating caller.
func dispatchHostTool(ctx context.Context, call hostCallFn, tool string, fields map[string]any) (string, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	res, err := call(ctx, tool, raw)
	if err != nil {
		return "", err
	}
	text := textFromContent(res.Content)
	if res.IsError {
		return "", fmt.Errorf("%s", strings.TrimSpace(text))
	}
	return text, nil
}

// hostBinding adapts one allowlisted host tool to a string-shaped
// jsengine.Binding, mapping positional script arguments onto the tool's
// JSON args.
func hostBinding(call hostCallFn, tool string, build func(args []string) (map[string]any, error)) jsengine.Binding {
	return func(ctx context.Context, args []string) (string, error) {
		fields, err := build(args)
		if err != nil {
			return "", err
		}
		return dispatchHostTool(ctx, call, tool, fields)
	}
}

// typedHostBinding is the same crossing for a binding whose arguments do
// not survive being flattened to strings — edit's array of replacements,
// for one.
func typedHostBinding(call hostCallFn, tool string, build func(args []any) (map[string]any, error)) jsengine.TypedBinding {
	return func(ctx context.Context, args []any) (any, error) {
		fields, err := build(args)
		if err != nil {
			return nil, err
		}
		return dispatchHostTool(ctx, call, tool, fields)
	}
}

// textFromContent flattens a tool result to the text a script receives.
// Non-text content isn't representable to a script expecting strings, so
// it is described rather than silently dropped (same stance as ext
// host_tool_call's contentBlocksFromResult).
func textFromContent(content []provider.Content) string {
	var b strings.Builder
	for _, c := range content {
		switch v := c.(type) {
		case provider.TextBlock:
			b.WriteString(v.Text)
		default:
			fmt.Fprintf(&b, "[%T content omitted]", c)
		}
	}
	return b.String()
}

func intArg(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("expected an integer, got %q", s)
	}
	return n, nil
}

func (t *CodeExecutionTool) bindings() map[string]jsengine.Binding {
	b := readOnlyScriptBindings(t.HostCall)
	db, _ := disclosureBindings(t.Catalog, t.HostCall)
	for name, fn := range db {
		b[name] = fn
	}
	return b
}

// typedBindings returns the catalog's call() binding, the one disclosure
// binding whose second argument is an object and cannot survive flattening
// to strings.
func (t *CodeExecutionTool) typedBindings() map[string]jsengine.TypedBinding {
	_, tb := disclosureBindings(t.Catalog, t.HostCall)
	return tb
}

// readOnlyScriptBindings is the read-only binding set. The mutating tool
// takes it as the base of its superset, so a script that only looks at
// the workspace behaves identically under either tool.
func readOnlyScriptBindings(call hostCallFn) map[string]jsengine.Binding {
	return map[string]jsengine.Binding{
		"read": hostBinding(call, "read", func(args []string) (map[string]any, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("read(path[,offset,limit]) needs a path")
			}
			m := map[string]any{"path": args[0]}
			if len(args) > 1 {
				n, err := intArg(args[1])
				if err != nil {
					return nil, err
				}
				m["offset"] = n
			}
			if len(args) > 2 {
				n, err := intArg(args[2])
				if err != nil {
					return nil, err
				}
				m["limit"] = n
			}
			return m, nil
		}),
		"grep": hostBinding(call, "grep", func(args []string) (map[string]any, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("grep(pattern[,path]) needs a pattern")
			}
			m := map[string]any{"pattern": args[0]}
			if len(args) > 1 {
				m["path"] = args[1]
			}
			return m, nil
		}),
		"glob": hostBinding(call, "glob", func(args []string) (map[string]any, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("glob(pattern[,path]) needs a pattern")
			}
			m := map[string]any{"pattern": args[0]}
			if len(args) > 1 {
				m["path"] = args[1]
			}
			return m, nil
		}),
	}
}

func (t *CodeExecutionTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a codeExecArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Script) == "" {
		return core.ToolResult{}, fmt.Errorf("script is required")
	}
	if t.HostCall == nil {
		// Fail closed: without the gated dispatcher the bindings would
		// have no permission path, so the tool must not run at all.
		return core.ToolResult{}, fmt.Errorf("code_execution is not wired to the approval gate in this session")
	}

	// The pre-check runs BEFORE the engine, and it is a precondition here too
	// — not because this tool writes files, but because call() now lets it
	// reach beyond read/grep/glob into the disclosed catalog. The account the
	// approval prompt shows is only as good as the pre-check behind it, so a
	// script the walker cannot read does not run.
	refs, aerr := jsengine.AnalyzeBindings("code_execution.js", a.Script, t.accountedNames())
	if aerr != nil {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("%v", aerr)}},
			IsError: true,
		}, nil
	}
	if !refs.Complete {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: unaccountableMessage(refs)}},
			IsError: true,
			Details: map[string]any{"accounted": false, "reasons": refs.Reasons},
		}, nil
	}

	timeoutDur := defaultCodeExecTimeout
	if a.Timeout > 0 {
		timeoutDur = min(time.Duration(a.Timeout)*time.Second, maxCodeExecTimeout)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	res, err := jsengine.Run(runCtx, "code_execution.js", a.Script, jsengine.Options{
		Bindings:      t.bindings(),
		TypedBindings: t.typedBindings(),
	})
	details := map[string]any{
		"host_calls": res.HostCalls,
		"elapsed_ms": res.Elapsed.Milliseconds(),
		"truncated":  res.Truncated,
		"timed_out":  res.TimedOut,
	}
	if err != nil {
		msg := fmt.Sprintf("%v", err)
		if res.TimedOut {
			msg = fmt.Sprintf("script timed out after %s (raise the timeout arg up to 120s, or narrow the work)", timeoutDur)
		}
		if res.Output != "" {
			msg += "\n\npartial output before the failure:\n" + res.Output
		}
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: msg}},
			IsError: true,
			Details: details,
		}, nil
	}
	text := res.Output
	if strings.TrimSpace(text) == "" {
		text = "(script completed but printed nothing — call print(...) with what you want returned)"
	} else if res.Truncated {
		text += fmt.Sprintf("\n[output truncated at %d bytes]", 32*1024)
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Details: details,
	}, nil
}
