//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/agent/jsengine"
)

// The disclosure bindings (§12.7): how a script discovers and reaches the
// tools in its catalog beyond the fixed read/grep/glob. tools() and
// describe() read the catalog in-engine and never touch a host tool, so they
// cost no approval gate and no audit line. call() is the opposite — it
// dispatches through HostCall exactly like the fixed bindings, so every
// disclosed call faces the same gate and audit as a model-issued one.

// disclosureBindings returns the three catalog bindings for a tool holding
// catalog and dispatcher. All three fail closed when catalog is nil (the
// tool is not wired), which Execute already guarantees cannot happen for a
// real run — a nil HostCall refuses first.
func disclosureBindings(catalog *DisclosureCatalog, call hostCallFn) (map[string]jsengine.Binding, map[string]jsengine.TypedBinding) {
	bindings := map[string]jsengine.Binding{
		"tools": func(ctx context.Context, args []string) (string, error) {
			if catalog == nil {
				return "", errNoCatalog()
			}
			return catalog.List(), nil
		},
		"describe": func(ctx context.Context, args []string) (string, error) {
			if catalog == nil {
				return "", errNoCatalog()
			}
			if len(args) < 1 {
				return "", fmt.Errorf("describe(name) needs a tool name")
			}
			e, err := catalog.Describe(args[0])
			if err != nil {
				return "", err
			}
			b, _ := json.MarshalIndent(e, "", "  ")
			return string(b), nil
		},
	}
	typed := map[string]jsengine.TypedBinding{
		"call": func(ctx context.Context, args []any) (any, error) {
			if catalog == nil {
				return nil, errNoCatalog()
			}
			if len(args) < 1 {
				return nil, fmt.Errorf(`call(name, args) needs a tool name as its first argument`)
			}
			name, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("call(name, args): the tool name must be a string literal, got %T", args[0])
			}
			if !catalog.MayCall(name) {
				return nil, &notDisclosedError{name: name}
			}
			var fields map[string]any
			if len(args) > 1 && args[1] != nil {
				fields, ok = args[1].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("call(%q, args): args must be an object, got %T", name, args[1])
				}
			}
			if fields == nil {
				fields = map[string]any{}
			}
			return dispatchHostTool(ctx, call, name, fields)
		},
	}
	return bindings, typed
}
