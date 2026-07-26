package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// identitySource is a tool source that also reports which extensions it loaded
// (the optional Identities() that MergeExtensionTools hands to terva_status).
type identitySource struct {
	fakeToolSource
	ids []tools.ExtensionIdentity
}

func (s *identitySource) Identities() []tools.ExtensionIdentity { return s.ids }

// TW-035. The gate, not the renderer: status.go can format an extension list
// perfectly and still report nothing, because the list only ever arrives if the
// merge binds it. Every surface funnels through MergeExtensionTools, so this is
// the one place that has to work — and a unit test on the tool cannot see it.
func TestMergeBindsExtensionIdentitiesToStatus(t *testing.T) {
	status := &tools.StatusTool{Provider: "anthropic", CWD: testsupport.TempDir(t)}
	r := &build.Resolved{
		ToolRegistry: core.Registry{"terva_status": status},
		CWD:          testsupport.TempDir(t),
	}

	r.MergeExtensionTools(&identitySource{ids: []tools.ExtensionIdentity{
		{Name: "jmap-mail", Version: "0.14.0"},
	}})

	out := statusText(t, status)
	if !strings.Contains(out, "extensions: jmap-mail v0.14.0") {
		t.Fatalf("merge did not bind the extension list to terva_status:\n%s", out)
	}
}

// The binding is a closure, not a snapshot: extensions reload and the
// approval-mode switch re-merges, so a value captured at merge time would go
// stale without anything saying so.
func TestStatusExtensionListTracksReloads(t *testing.T) {
	status := &tools.StatusTool{CWD: testsupport.TempDir(t)}
	r := &build.Resolved{
		ToolRegistry: core.Registry{"terva_status": status},
		CWD:          testsupport.TempDir(t),
	}
	src := &identitySource{ids: []tools.ExtensionIdentity{{Name: "jmap-mail", Version: "0.13.0"}}}
	r.MergeExtensionTools(src)

	// The extension reloads at a new version; nothing re-merges.
	src.ids = []tools.ExtensionIdentity{{Name: "jmap-mail", Version: "0.14.0"}}

	out := statusText(t, status)
	if strings.Contains(out, "0.13.0") {
		t.Errorf("status reported a version the session no longer runs:\n%s", out)
	}
	if !strings.Contains(out, "jmap-mail v0.14.0") {
		t.Errorf("status did not pick up the reloaded version:\n%s", out)
	}
}

// A source with no identities to report (MCP) leaves the line off entirely
// rather than claiming an empty set — an MCP server declares no version terva
// can vouch for, and "extensions: none" would read as "you have none loaded".
func TestStatusOmitsTheLineForASourceWithoutIdentities(t *testing.T) {
	status := &tools.StatusTool{CWD: testsupport.TempDir(t)}
	r := &build.Resolved{
		ToolRegistry: core.Registry{"terva_status": status},
		CWD:          testsupport.TempDir(t),
	}
	r.MergeExtensionTools(&fakeToolSource{})

	if out := statusText(t, status); strings.Contains(out, "extensions:") {
		t.Errorf("a source reporting no identities still produced a line:\n%s", out)
	}
}

func statusText(t *testing.T, s *tools.StatusTool) string {
	t.Helper()
	res, err := s.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("terva_status: %v", err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}
