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

// binding adapts one allowlisted host tool to a jsengine.Binding, mapping
// positional script arguments onto the tool's JSON args.
func (t *CodeExecutionTool) binding(tool string, build func(args []string) (map[string]any, error)) jsengine.Binding {
	return func(ctx context.Context, args []string) (string, error) {
		fields, err := build(args)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return "", err
		}
		res, err := t.HostCall(ctx, tool, raw)
		if err != nil {
			return "", err
		}
		text := textFromContent(res.Content)
		if res.IsError {
			return "", fmt.Errorf("%s", strings.TrimSpace(text))
		}
		return text, nil
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
	return map[string]jsengine.Binding{
		"read": t.binding("read", func(args []string) (map[string]any, error) {
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
		"grep": t.binding("grep", func(args []string) (map[string]any, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("grep(pattern[,path]) needs a pattern")
			}
			m := map[string]any{"pattern": args[0]}
			if len(args) > 1 {
				m["path"] = args[1]
			}
			return m, nil
		}),
		"glob": t.binding("glob", func(args []string) (map[string]any, error) {
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
	timeoutDur := defaultCodeExecTimeout
	if a.Timeout > 0 {
		timeoutDur = min(time.Duration(a.Timeout)*time.Second, maxCodeExecTimeout)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	res, err := jsengine.Run(runCtx, "code_execution.js", a.Script, jsengine.Options{
		Bindings: t.bindings(),
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
