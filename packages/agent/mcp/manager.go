package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// ToolInfo is one MCP tool ready for the agent's registry, already
// namespaced.
type ToolInfo struct {
	Server      string
	Name        string // namespaced: mcp_<server>_<tool>
	Description string
	Schema      json.RawMessage
	// ReadOnly mirrors the server's readOnlyHint annotation.
	ReadOnly bool
}

// NamespaceTool builds the registry name for a server's tool. The
// mcp_ prefix keeps origins visible in transcripts, permission rules
// ("mcp_*" globs) and collision messages — the ownership-metadata
// lesson from Goose (docs/plans/harness-landscape-2026.md A1).
func NamespaceTool(server, tool string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
				return r
			}
			return '_'
		}, s)
	}
	return "mcp_" + clean(server) + "_" + clean(tool)
}

// Manager owns the configured MCP server subprocesses for one run.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
	// routes maps the namespaced registry name back to its server and
	// original tool name.
	routes map[string]route
	warns  []string
	// cwd is the project directory every server subprocess runs in,
	// captured at StartAll so a mid-session StartOne inherits it too.
	cwd string
	// onToolsChanged, if set, is invoked (off-lock) after a server dies
	// unexpectedly and its routes are withdrawn, so the host can rebuild the
	// live tool registry. Mirrors the extension manager's SetOnReload.
	onToolsChanged func()
}

type route struct {
	server string
	tool   string
}

// StartAll spawns every configured server and lists its tools.
// Failures are per-server warnings, never fatal: one broken MCP
// server must not take the session down. cwd is the project directory
// the servers run in (empty inherits terva's process cwd). stderrFor
// supplies a log sink per server (nil means discard).
func StartAll(ctx context.Context, cfg *Config, cwd string, stderrFor func(server string) io.Writer) *Manager {
	m := &Manager{clients: map[string]*Client{}, routes: map[string]route{}, cwd: cwd}
	if cfg == nil || len(cfg.Servers) == 0 {
		return m
	}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic startup + collision order

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string, sc ServerConfig) {
			defer wg.Done()
			var stderr io.Writer
			if stderrFor != nil {
				stderr = stderrFor(name)
			}
			cl, err := Start(ctx, name, sc, m.cwd, stderr)
			m.mu.Lock()
			defer m.mu.Unlock()
			if err != nil {
				m.warns = append(m.warns, fmt.Sprintf("mcp server %s: %v", name, err))
				return
			}
			m.clients[name] = cl
			go m.watch(name, cl)
		}(name, cfg.Servers[name])
	}
	wg.Wait()

	// Index tools after all servers are up, in sorted-server order so
	// collisions resolve deterministically (first registration wins,
	// same convention as extensions).
	m.mu.Lock()
	for _, name := range names {
		if cl := m.clients[name]; cl != nil {
			m.indexRoutesLocked(name, cl)
		}
	}
	m.mu.Unlock()
	return m
}

// indexRoutesLocked registers (or re-registers) the namespaced routes
// for one already-started client. The caller holds m.mu. First
// registration wins on collision with a *different* server — the same
// convention StartAll's sorted pass uses — and a tool whose namespace is
// already taken is recorded as shadowed. Re-indexing the same server is
// harmless (its own routes overwrite themselves).
func (m *Manager) indexRoutesLocked(name string, cl *Client) {
	for _, t := range cl.Tools() {
		ns := NamespaceTool(name, t.Name)
		if r, taken := m.routes[ns]; taken && r.server != name {
			m.warns = append(m.warns, fmt.Sprintf("mcp server %s: tool %s shadowed (name %s already registered)", name, t.Name, ns))
			continue
		}
		m.routes[ns] = route{server: name, tool: t.Name}
	}
}

// clearServerWarnsLocked drops any accumulated warnings for one server so
// a repeated StartOne/StopOne cycle doesn't pile up stale messages. The
// caller holds m.mu.
func (m *Manager) clearServerWarnsLocked(name string) {
	prefix := "mcp server " + name + ":"
	kept := m.warns[:0]
	for _, w := range m.warns {
		if !strings.HasPrefix(w, prefix) {
			kept = append(kept, w)
		}
	}
	m.warns = kept
}

// StartOne spawns a single configured server mid-session and indexes its
// tools. Idempotent: a server already running is left untouched. On
// failure the error is recorded as a warning *and* returned, so the
// caller (the /mcp dialog) can surface "failed to start" while the
// session carries on. stderr supplies the per-server log sink (nil means
// discard). Intended for single-threaded callers (the TUI loop).
func (m *Manager) StartOne(ctx context.Context, name string, sc ServerConfig, stderr io.Writer) error {
	if m == nil {
		return fmt.Errorf("mcp manager is nil")
	}
	m.mu.Lock()
	_, running := m.clients[name]
	m.mu.Unlock()
	if running {
		return nil
	}
	cl, err := Start(ctx, name, sc, m.cwd, stderr)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearServerWarnsLocked(name)
	if err != nil {
		m.warns = append(m.warns, fmt.Sprintf("mcp server %s: %v", name, err))
		return err
	}
	m.clients[name] = cl
	m.indexRoutesLocked(name, cl)
	go m.watch(name, cl)
	return nil
}

// SetOnToolsChanged registers a callback invoked (off-lock) after the set of
// available tools changes because a server died unexpectedly and its tools
// were withdrawn. The host wires this to its live tool-registry rebuild
// (the same seam extension reloads use). Nil disables it.
func (m *Manager) SetOnToolsChanged(fn func()) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onToolsChanged = fn
	m.mu.Unlock()
}

// watch waits for one client's read loop to exit. If the client is still the
// one registered under its name, the exit was an unexpected death (StopOne and
// StopAll delete from the map before stopping, so an intentional shutdown is
// already gone here): withdraw its routes, drop it so Status/Tools stop
// reporting it, record a warning, and ask the host to refresh its tools.
// Automatic restart is intentionally left to a later feature — truthful status
// and discovery are the requirement.
func (m *Manager) watch(name string, cl *Client) {
	<-cl.Done()
	m.mu.Lock()
	if m.clients[name] != cl {
		// Already removed by an intentional Stop — not a crash.
		m.mu.Unlock()
		return
	}
	delete(m.clients, name)
	for ns, r := range m.routes {
		if r.server == name {
			delete(m.routes, ns)
		}
	}
	m.warns = append(m.warns, fmt.Sprintf("mcp server %s: connection closed unexpectedly; its tools were withdrawn", name))
	onChanged := m.onToolsChanged
	m.mu.Unlock()
	cl.Stop() // reap the dead subprocess (closed is already set, so this is fast)
	if onChanged != nil {
		onChanged()
	}
}

// StopOne stops a single running server and removes its routes. No-op if
// the server isn't running. Client.Stop (which can block up to 2s) runs
// off-lock, matching StopAll.
func (m *Manager) StopOne(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	cl := m.clients[name]
	delete(m.clients, name)
	for ns, r := range m.routes {
		if r.server == name {
			delete(m.routes, ns)
		}
	}
	m.clearServerWarnsLocked(name)
	m.mu.Unlock()
	if cl != nil {
		cl.Stop()
	}
}

// ServerStatus is one server's live state for the /mcp dialog.
type ServerStatus struct {
	Name      string
	Connected bool   // a live client is held (spawned + handshook + listed)
	ToolCount int    // routed (non-shadowed) tools currently exposed
	Info      string // serverInfo name reported at initialize, if any
}

// Status reports the live state of every server the manager currently
// holds a client for. Configured-but-disabled or failed-to-start servers
// have no client and are absent here, so the dialog overlays this on the
// authoritative config-driven row set rather than treating it as the row
// set itself.
func (m *Manager) Status() []ServerStatus {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServerStatus, 0, len(m.clients))
	for _, name := range sortedKeys(m.clients) {
		count := 0
		for _, r := range m.routes {
			if r.server == name {
				count++
			}
		}
		out = append(out, ServerStatus{Name: name, Connected: true, ToolCount: count, Info: m.clients[name].serverInfo})
	}
	return out
}

// Warnings returns startup problems for the host to surface.
func (m *Manager) Warnings() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.warns...)
}

// Tools lists every available namespaced tool.
func (m *Manager) Tools() []ToolInfo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ToolInfo
	for _, name := range sortedKeys(m.clients) {
		cl := m.clients[name]
		for _, t := range cl.Tools() {
			ns := NamespaceTool(name, t.Name)
			if r, ok := m.routes[ns]; !ok || r.server != name {
				continue // shadowed
			}
			out = append(out, ToolInfo{Server: name, Name: ns, Description: t.Description, Schema: t.InputSchema, ReadOnly: t.Annotations.ReadOnlyHint})
		}
	}
	return out
}

// Call routes a namespaced tool call to its server.
func (m *Manager) Call(ctx context.Context, namespaced string, args json.RawMessage) (CallResult, error) {
	m.mu.Lock()
	r, ok := m.routes[namespaced]
	cl := m.clients[r.server]
	m.mu.Unlock()
	if !ok || cl == nil {
		return CallResult{}, fmt.Errorf("unknown mcp tool %s", namespaced)
	}
	return cl.CallTool(ctx, r.tool, args)
}

// StopAll shuts every server down.
func (m *Manager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*Client{}
	m.mu.Unlock()
	for _, c := range clients {
		c.Stop()
	}
}

// Tool adapts one MCP tool to core.Tool for the agent registry.
type Tool struct {
	mgr  *Manager
	info ToolInfo
}

// NewTool builds the registry adapter for a namespaced tool.
func (m *Manager) NewTool(info ToolInfo) *Tool { return &Tool{mgr: m, info: info} }

func (t *Tool) Name() string        { return t.info.Name }
func (t *Tool) Description() string { return t.info.Description }

// Extension reports this tool's capability group for lazy tool visibility
// (retro H2·b): "mcp:<server>", mirroring the label the build merge assigns
// (build/wiring.go). It matches the accessor an extension tool exposes, so a
// group classifier can treat MCP and extension tools uniformly by interface.
func (t *Tool) Extension() string { return "mcp:" + t.info.Server }
func (t *Tool) Schema() json.RawMessage {
	if len(t.info.Schema) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return t.info.Schema
}

func (t *Tool) Execute(ctx context.Context, args json.RawMessage, progress func(string)) (core.ToolResult, error) {
	res, err := t.mgr.Call(ctx, t.info.Name, args)
	if err != nil {
		return core.ToolResult{}, err
	}
	var content []provider.Content
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			content = append(content, provider.TextBlock{Text: c.Text})
		case "image":
			if raw, derr := decodeBase64(c.Data); derr == nil {
				content = append(content, provider.ImageBlock{MimeType: c.MimeType, Data: raw})
			} else {
				content = append(content, provider.TextBlock{Text: "[image from MCP server dropped: undecodable base64]"})
			}
		default:
			// Resources and future content kinds degrade to their JSON
			// so nothing is silently lost.
			b, _ := json.Marshal(c)
			content = append(content, provider.TextBlock{Text: string(b)})
		}
	}
	if len(content) == 0 {
		content = append(content, provider.TextBlock{Text: ""})
	}
	return core.ToolResult{Content: content, IsError: res.IsError}, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
