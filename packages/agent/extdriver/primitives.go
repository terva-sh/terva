package extdriver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

// Commands returns a snapshot of every (extension, command) pair
// currently registered. Used by the slash autocomplete + /help.
func (d *Driver) Commands() []CommandInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var out []CommandInfo
	for _, ext := range d.ext {
		for _, c := range ext.commands {
			out = append(out, CommandInfo{
				Extension:   ext.Manifest.Name,
				Name:        c.Name,
				Description: c.Description,
			})
		}
	}
	return out
}

// CommandInfo is one extension-registered slash command, surfaced to
// the rest of terva for display purposes.
type CommandInfo struct {
	Extension   string
	Name        string
	Description string
}

// ToolInfo is one extension-registered tool. Used by the agent's
// build step to materialise core.Tool wrappers.
type ToolInfo struct {
	Extension   string
	Name        string
	Description string
	Schema      json.RawMessage
	// ReadOnly carries the extension's no-side-effects declaration
	// (register_tool read_only) into the host's permission
	// classification.
	ReadOnly bool
	// Authority carries the extension's effect-class declaration
	// (register_tool authority; core.Authority). Richer than ReadOnly:
	// when set it decides read-only classification (only local-read is
	// auto-allowable), so a network-read tool is not mistaken for a
	// local read. Empty falls back to ReadOnly.
	Authority string
	// Essential carries the extension's load-bearing declaration
	// (register_tool essential): the tool stays advertised every turn even
	// under lazy tool visibility instead of deferring behind activate_tools.
	// Already capped per extension at registration (registerTool), so this
	// reflects the honored value, not the raw request.
	Essential bool
	// Withdrawn is set when the owning extension hid this tool for the
	// session (set_withdrawn_tools, protocol 4). The model-facing merge
	// feed skips withdrawn tools so they leave the registry and the system
	// prompt; the /extensions dialog ignores the flag and keeps showing the
	// full registered count. The tool stays indexed (HasTool), so a later
	// restore needs no re-registration.
	Withdrawn bool
}

// Tools returns a snapshot of every (extension, tool) pair currently
// registered. Used at agent-build time to fold extension tools into
// the runtime tool registry.
func (d *Driver) Tools() []ToolInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var out []ToolInfo
	for _, ext := range d.ext {
		for _, t := range ext.tools {
			out = append(out, ToolInfo{
				Extension:   ext.Manifest.Name,
				Name:        t.Name,
				Description: t.Description,
				Schema:      t.Schema,
				ReadOnly:    t.ReadOnly,
				Authority:   t.Authority,
				Essential:   t.Essential,
				Withdrawn:   ext.withdrawnTools[t.Name], // nil-map read is false
			})
		}
	}
	return out
}

// HasTool reports whether name is registered by any extension.
func (d *Driver) HasTool(name string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.toolIndex[name]
	return ok
}

// InvokeTool sends a tool_call to the owning extension and waits for
// the matching tool_result. Used by the core.Tool wrapper that the
// agent registers per extension-defined tool.
func (d *Driver) InvokeTool(ctx context.Context, name string, args json.RawMessage, timeout time.Duration) (extproto.ToolResultFromExt, error) {
	d.mu.RLock()
	ext, ok := d.toolIndex[name]
	d.mu.RUnlock()
	if !ok {
		return extproto.ToolResultFromExt{}, fmt.Errorf("no extension registered for tool %q", name)
	}

	// Cap the outbound args. A frame larger than the extension's read
	// limit would kill it mid-read (and InvokeTool would only find out
	// when it timed out, ~60s later). Return a normal tool error the
	// model can recover from instead, and never send the oversized frame.
	if len(args) > extproto.MaxToolCallBytes {
		return extproto.ToolResultFromExt{
			IsError: true,
			Content: []extproto.ContentBlock{{
				Type: "text",
				Text: fmt.Sprintf("tool %q arguments are %d bytes; the extension frame limit is %d bytes — send less data",
					name, len(args), extproto.MaxToolCallBytes),
			}},
		}, nil
	}

	id := newCorrelationID()
	ch := make(chan extproto.ToolResultFromExt, 1)
	ext.mu.Lock()
	ext.pendingTool[id] = ch
	ext.mu.Unlock()

	if err := ext.writeFrame(extproto.ToolCallFromHost{
		Type: "tool_call",
		ID:   id,
		Name: name,
		Args: args,
	}); err != nil {
		ext.mu.Lock()
		delete(ext.pendingTool, id)
		ext.mu.Unlock()
		return extproto.ToolResultFromExt{}, fmt.Errorf("write: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		ext.mu.Lock()
		delete(ext.pendingTool, id)
		ext.mu.Unlock()
		return extproto.ToolResultFromExt{}, fmt.Errorf("timeout waiting for %s/%s", ext.Manifest.Name, name)
	case <-ctx.Done():
		ext.mu.Lock()
		delete(ext.pendingTool, id)
		ext.mu.Unlock()
		return extproto.ToolResultFromExt{}, ctx.Err()
	}
}

// handleHostToolCall runs an extension's host_tool_call through the
// injected dispatcher and replies with a host_tool_result correlated by
// the extension's own id. The dispatch runs on a fresh goroutine because
// it may block on an approval prompt and the read loop must keep
// reading; writeFrame is safe to call concurrently (it enqueues onto the
// single-writer outbox). A nil dispatcher means host tool calls are
// unsupported, answered with an error result so the extension's script
// fails cleanly instead of hanging.
func (d *Driver) handleHostToolCall(ext *Extension, htc extproto.HostToolCallFromExt) {
	d.mu.RLock()
	dispatch := d.hostToolDispatch
	d.mu.RUnlock()

	reply := func(content []extproto.ContentBlock, isErr bool) {
		_ = ext.writeFrame(extproto.HostToolResultFromHost{
			Type:    "host_tool_result",
			ID:      htc.ID,
			Content: content,
			IsError: isErr,
		})
	}

	if dispatch == nil {
		reply([]extproto.ContentBlock{{Type: "text", Text: "host tool calls are not supported by this host"}}, true)
		return
	}
	go func() {
		content, isErr := dispatch(context.Background(), ext.Manifest.Name, htc.Name, htc.Args, htc.Silent)
		reply(content, isErr)
	}()
}

// handleListSessions serves an extension's list_sessions via the
// injected reader and replies with session_list, correlated by the
// extension's id. Off the read-loop goroutine in case the reader touches
// disk. A nil reader yields an empty list (session reads unsupported).
func (d *Driver) handleListSessions(ext *Extension, ls extproto.ListSessionsFromExt) {
	d.mu.RLock()
	reader := d.sessionReader
	d.mu.RUnlock()
	go func() {
		var sessions []extproto.SessionInfo
		if reader != nil {
			sessions = reader.ListSessions(ext.Manifest.Name, ls.ProjectID)
		}
		_ = ext.writeFrame(extproto.SessionListFromHost{Type: "session_list", ID: ls.ID, Sessions: sessions})
	}()
}

// handleReadSession serves an extension's read_session via the injected
// reader and replies with session_data. A nil reader or an unknown /
// out-of-project id yields not_found.
func (d *Driver) handleReadSession(ext *Extension, rs extproto.ReadSessionFromExt) {
	d.mu.RLock()
	reader := d.sessionReader
	d.mu.RUnlock()
	go func() {
		if reader == nil {
			_ = ext.writeFrame(extproto.SessionDataFromHost{Type: "session_data", ID: rs.ID, NotFound: true})
			return
		}
		msgs, found := reader.ReadSession(ext.Manifest.Name, rs.SessionID)
		_ = ext.writeFrame(extproto.SessionDataFromHost{Type: "session_data", ID: rs.ID, Messages: msgs, NotFound: !found})
	}()
}

// HasCommand reports whether name is registered by any extension.
func (d *Driver) HasCommand(name string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.commandIndex[name]
	return ok
}

// CommandOwner returns the manifest name of the extension that owns the
// named slash command, or "" if none.
func (d *Driver) CommandOwner(name string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if ext, ok := d.commandIndex[name]; ok && ext != nil {
		return ext.Manifest.Name
	}
	return ""
}

// Invoke fires the named slash command's handler in the owning
// extension and waits up to timeout for the response. Returns the
// extension's CommandResponse so the caller can act on the action
// (prompt / insert / display).
func (d *Driver) Invoke(ctx context.Context, name, args string, timeout time.Duration) (extproto.CommandResponseFromExt, error) {
	d.mu.RLock()
	ext, ok := d.commandIndex[name]
	d.mu.RUnlock()
	if !ok {
		return extproto.CommandResponseFromExt{}, fmt.Errorf("no extension registered for /%s", name)
	}

	id := newCorrelationID()
	ch := make(chan extproto.CommandResponseFromExt, 1)
	ext.mu.Lock()
	ext.pending[id] = ch
	ext.mu.Unlock()

	if err := ext.writeFrame(extproto.CommandInvokedFromHost{
		Type: "command_invoked",
		ID:   id,
		Name: name,
		Args: args,
	}); err != nil {
		ext.mu.Lock()
		delete(ext.pending, id)
		ext.mu.Unlock()
		return extproto.CommandResponseFromExt{}, fmt.Errorf("write: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		ext.mu.Lock()
		delete(ext.pending, id)
		ext.mu.Unlock()
		return extproto.CommandResponseFromExt{}, fmt.Errorf("timeout waiting for %s/%s", ext.Manifest.Name, name)
	case <-ctx.Done():
		ext.mu.Lock()
		delete(ext.pending, id)
		ext.mu.Unlock()
		return extproto.CommandResponseFromExt{}, ctx.Err()
	}
}

// corrSeq makes correlation IDs collision-free across concurrent
// callers. The wall-clock prefix alone (microsecond resolution) was not
// unique enough: a tight burst of concurrent InvokeTool / intercept /
// command calls could mint the same ID within one microsecond, and the
// second registration would overwrite the first's reply channel in the
// pending map — so one caller received the other's response (or none)
// and hung. See the frame-integrity test in manager_test.go.
var corrSeq atomic.Uint64

// newCorrelationID returns a process-unique id for matching a host
// request to its extension response. The timestamp keeps ids readable
// in the per-extension log; the atomic counter guarantees uniqueness.
func newCorrelationID() string {
	ts := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	return ts + "-" + strconv.FormatUint(corrSeq.Add(1), 10)
}
