//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/jsengine"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// CodeExecutionMutatingTool is code_execution's mutating twin: the same
// engine and the same gated crossing, with write and edit added to the
// binding set.
//
// It is a SEPARATE TOOL rather than a wider mode of code_execution, and
// that is the whole design (docs/proposals/jsengine-general-runtime.md
// §12.1). Authority stays a property of the tool, which is where the rest
// of the system already looks for it: code_execution keeps its read-only
// class and its plan-mode survival unconditionally, instead of holding a
// classification that has to be computed from an argument. Going from
// "reads the repo" to "writes files" deserves its own call rather than a
// flag a reviewer skims past, and the name in a transcript, an audit
// line, or a permission rule then says what happened.
//
// The binding set stops at write/edit. bash is deliberately absent (§12.4):
// with a command string in the set, the interesting authority hides where
// the pre-check below cannot read it, and the tool becomes bash with extra
// steps. What is here instead is a ceiling that can actually be checked.
type CodeExecutionMutatingTool struct {
	// HostCall runs one of the host's own tools through the SAME approval
	// gate a model-issued call uses (reach, not authority). Late-bound in
	// build.WireHostToolDispatcher; Execute fails closed when unset.
	HostCall func(ctx context.Context, tool string, args json.RawMessage) (core.ToolResult, error)
}

// mutatingScriptBindings is the full binding set, and the exact list the
// pre-check accounts for. Order is the reporting order.
var mutatingScriptBindings = []string{"read", "grep", "glob", "write", "edit"}

const codeExecMutatingSchema = `{"type":"object","properties":{"script":{"type":"string","description":"A short JavaScript program. To examine the workspace, call read(path[,offset,limit]), grep(pattern[,path]), and glob(pattern[,path]). To change it, call write(path, content[, mode]) and edit(path, edits). Each edit is an object with oldText and newText, and an optional replaceAll flag. Use print(...) to return a result. Call each of these functions directly by name."},"timeout":{"type":"integer","description":"The maximum run time in seconds. The tool then stops the program. The default is 30 seconds, and the maximum is 120 seconds."}},"required":["script"]}`

func (t *CodeExecutionMutatingTool) Name() string { return "code_execution_mutating" }

func (t *CodeExecutionMutatingTool) Description() string {
	return i18n.D("tool.code_execution_mutating.description", "Run a short JavaScript program that can change files.\n\nThe program examines the workspace with read(path[,offset,limit]), grep(pattern[,path]), and glob(pattern[,path]). It changes the workspace with write(path, content[, mode]) and edit(path, edits). Use print(...) to return a result.\n\nUse this tool for a task that makes many small changes, when each change follows from what the program reads. For a task that only reads, use code_execution instead.\n\nThe program cannot run commands. The tool does not provide bash.\n\nEvery call goes through the usual permission gate. Each change asks for approval in the same way as a direct call.\n\nThe tool examines the program before it runs it, and it reports the calls that the program makes. The tool refuses a program that it cannot account for. Therefore call each function directly by its name. Do not use eval, do not reach the global object, and do not copy a function into a variable.\n\nThe program can make 50 calls to the host and can print 32KB. The default time limit is 30 seconds.")
}

func (t *CodeExecutionMutatingTool) Schema() json.RawMessage {
	return json.RawMessage(codeExecMutatingSchema)
}

// ToolGroupName puts the mutating tool in its OWN lazily-activated group,
// not alongside code_execution in "scripting". Sharing a group would mean
// that a session reaching for read-only scripting is handed a tool that
// writes files in the same act — the quiet widening that §12.1 refused.
// Activating this group is a separate, visible decision.
func (t *CodeExecutionMutatingTool) ToolGroupName() string { return "scripting_mutating" }

// bindings is the read-only base of the superset, identical to
// code_execution's, so a script that only reads behaves the same here.
func (t *CodeExecutionMutatingTool) bindings() map[string]jsengine.Binding {
	return readOnlyScriptBindings(t.HostCall)
}

// typedBindings are the mutating additions. They are typed rather than
// string-shaped because edit's replacement list is an array of objects,
// which does not survive being flattened to a string at all.
func (t *CodeExecutionMutatingTool) typedBindings() map[string]jsengine.TypedBinding {
	return map[string]jsengine.TypedBinding{
		"write": typedHostBinding(t.HostCall, "write", func(args []any) (map[string]any, error) {
			path, err := scriptStringArg(args, 0, "write(path, content[, mode])", "path")
			if err != nil {
				return nil, err
			}
			content, err := scriptStringArg(args, 1, "write(path, content[, mode])", "content")
			if err != nil {
				return nil, err
			}
			m := map[string]any{"path": path, "content": content}
			if len(args) > 2 && args[2] != nil {
				mode, ok := args[2].(string)
				if !ok {
					return nil, fmt.Errorf("write: mode must be a string, for example \"0755\"")
				}
				m["mode"] = mode
			}
			return m, nil
		}),
		"edit": typedHostBinding(t.HostCall, "edit", func(args []any) (map[string]any, error) {
			path, err := scriptStringArg(args, 0, "edit(path, edits)", "path")
			if err != nil {
				return nil, err
			}
			if len(args) < 2 || args[1] == nil {
				return nil, fmt.Errorf(`edit(path, edits) needs an array of edits, for example [{oldText: "a", newText: "b"}]`)
			}
			list, ok := args[1].([]any)
			if !ok {
				return nil, fmt.Errorf(`edit(path, edits): edits must be an array, for example [{oldText: "a", newText: "b"}]`)
			}
			if len(list) == 0 {
				return nil, fmt.Errorf("edit(path, edits) needs at least one edit")
			}
			// Validate here rather than letting the host tool reject the
			// JSON: a script author gets the position of the bad entry and
			// a catchable error, not a schema complaint about a payload
			// they never wrote by hand.
			edits := make([]any, 0, len(list))
			for i, raw := range list {
				m, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("edit: entry %d is not an object", i+1)
				}
				oldText, ok := m["oldText"].(string)
				if !ok {
					return nil, fmt.Errorf("edit: entry %d needs oldText as a string", i+1)
				}
				newText, ok := m["newText"].(string)
				if !ok {
					return nil, fmt.Errorf("edit: entry %d needs newText as a string", i+1)
				}
				entry := map[string]any{"oldText": oldText, "newText": newText}
				if v, present := m["replaceAll"]; present && v != nil {
					b, ok := v.(bool)
					if !ok {
						return nil, fmt.Errorf("edit: entry %d needs replaceAll as true or false", i+1)
					}
					entry["replaceAll"] = b
				}
				edits = append(edits, entry)
			}
			return map[string]any{"path": path, "edits": edits}, nil
		}),
	}
}

func scriptStringArg(args []any, i int, sig, field string) (string, error) {
	if len(args) <= i || args[i] == nil {
		return "", fmt.Errorf("%s needs %s", sig, field)
	}
	s, ok := args[i].(string)
	if !ok {
		return "", fmt.Errorf("%s: %s must be a string", sig, field)
	}
	return s, nil
}

// bindingPlan renders an accounted analysis as the line a human or a
// transcript reads: "read x5, write x2".
func bindingPlan(refs jsengine.BindingRefs) string {
	parts := make([]string, 0, len(mutatingScriptBindings))
	for _, name := range mutatingScriptBindings {
		if n := refs.Calls[name]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s x%d", name, n))
		}
	}
	if len(parts) == 0 {
		return "no host calls"
	}
	return strings.Join(parts, ", ")
}

func (t *CodeExecutionMutatingTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
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
		return core.ToolResult{}, fmt.Errorf("code_execution_mutating is not wired to the approval gate in this session")
	}

	// The pre-check runs BEFORE the engine, and it is a precondition
	// rather than a note in the margin. A tool that can write files should
	// be able to say what it is about to do; when it cannot say, it does
	// not run. That is the difference between an approval artifact and an
	// advisory one, and it is why the binding set excludes bash — a
	// command string would make every such account a guess.
	//
	// This applies even to a script that never calls write: the tool's
	// reach is what is being accounted for, not any one run's luck.
	refs, aerr := jsengine.AnalyzeBindings("code_execution_mutating.js", a.Script, mutatingScriptBindings)
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

	plan := bindingPlan(refs)
	if progress != nil {
		progress("accounted for: " + plan)
	}

	timeoutDur := defaultCodeExecTimeout
	if a.Timeout > 0 {
		timeoutDur = min(time.Duration(a.Timeout)*time.Second, maxCodeExecTimeout)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	res, err := jsengine.Run(runCtx, "code_execution_mutating.js", a.Script, jsengine.Options{
		Bindings:      t.bindings(),
		TypedBindings: t.typedBindings(),
	})
	details := map[string]any{
		"host_calls":   res.HostCalls,
		"elapsed_ms":   res.Elapsed.Milliseconds(),
		"truncated":    res.Truncated,
		"timed_out":    res.TimedOut,
		"accounted":    true,
		"binding_plan": plan,
	}
	if err != nil {
		msg := fmt.Sprintf("%v", err)
		if res.TimedOut {
			msg = fmt.Sprintf("script timed out after %s (raise the timeout arg up to 120s, or narrow the work)", timeoutDur)
		}
		if res.Output != "" {
			msg += "\n\npartial output before the failure:\n" + res.Output
		}
		// A failure mid-script may have already written files. Say so:
		// the model must not assume a failed run left nothing behind.
		if res.HostCalls > 0 {
			msg += fmt.Sprintf("\n\nthe script made %d host call(s) before it failed, so some changes may already be on disk", res.HostCalls)
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

// unaccountableMessage explains a refusal in terms of the fix. The
// reasons come from the walker already deduplicated and sorted.
func unaccountableMessage(refs jsengine.BindingRefs) string {
	var b strings.Builder
	b.WriteString("this script was not run, because its host calls cannot be accounted for before it runs:\n")
	reasons := append([]string(nil), refs.Reasons...)
	sort.Strings(reasons)
	for _, r := range reasons {
		b.WriteString("  - ")
		b.WriteString(r)
		b.WriteString("\n")
	}
	b.WriteString("\nRewrite the script so every host call is a direct call by name, for example write(\"a.txt\", body) rather than a copy of write held in a variable. A tool that changes files states what it will do first, so a script it cannot read is refused rather than run.")
	return b.String()
}
