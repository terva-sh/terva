package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// FilePublisher is the small affordance ShareFileTool calls into. The real
// implementation lives in the workspace host and writes into the share store
// (packages/agent/attach); tests can pass any stub.
//
// It is an interface for the same reason ChatSender is: the tool layer stays
// free of the store, and "is there anywhere to share TO" becomes a property of
// the host rather than a build-tag question inside a tool.
type FilePublisher interface {
	// Publish copies the file at path into the session's share area and
	// returns the record naming it. name relabels the file for the user;
	// empty keeps path's base. CallID is left unset — the agent loop stamps it.
	Publish(ctx context.Context, path, name string) (core.SharedFile, error)
}

// ShareFileTool hands a local file to the human reading the transcript: the web
// panel renders it as an image to look at, a clip to play, or a chip to
// download. It is the answer to "the agent made something and the user is not on
// this machine", which until now had none — the model could only name a path,
// which is useless from a browser three time zones away.
//
// Registered by the workspace host for every session it serves. Deliberately NOT
// the same thing as chat_send_file, which pushes into a paired chat service:
// this one publishes to whoever is holding the session.
//
// It classifies read-only (build/permissions.go) on the deliver_result
// rationale: it writes only terva's own state dir under $TERVA_HOME, never the
// workspace, so it never shows up in a diff — and a plan-mode session that has
// drafted something for the user should still be able to hand it over.
type ShareFileTool struct {
	CWD       string
	Sandbox   *Sandbox
	Publisher FilePublisher
}

type shareFileArgs struct {
	Path    string `json:"path"`
	Name    string `json:"name,omitempty"`
	Caption string `json:"caption,omitempty"`
}

const shareFileSchema = `{"type":"object","properties":{` +
	`"path":{"type":"string","description":"The path to a local file. Give an absolute path, or a path relative to the working directory."},` +
	`"name":{"type":"string","description":"An optional file name to show to the user instead of the name of the file."},` +
	`"caption":{"type":"string","description":"An optional note of one line. The tool shows it with the file."}` +
	`},"required":["path"]}`

func (t *ShareFileTool) Name() string { return "share_file" }

func (t *ShareFileTool) Description() string {
	return i18n.D("tool.share_file.description", "Send a local file to the user in the conversation. The user can look at an image, play audio or video, or download a file. Use this tool when the user must have or see the file itself, for example an export, a report, a chart, or a clip. Do not use it when a path that the user must open is sufficient. The tool sends a copy, and a later change to the original file does not change the copy.")
}

func (t *ShareFileTool) Schema() json.RawMessage { return json.RawMessage(shareFileSchema) }

func (t *ShareFileTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a shareFileArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" {
		return core.ToolResult{}, fmt.Errorf("path is required")
	}
	// No publisher means no session to share into (a non-workspace host). A
	// tool error rather than a hard failure: the model can say so in prose and
	// carry on, which is better than the turn dying over a display concern.
	if t.Publisher == nil {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: "this session has no one to share files with"}},
		}, nil
	}
	path := resolvePath(t.CWD, a.Path)
	// CheckPathRead, not CheckPath: sharing is a read of the source. A jailed
	// agent can hand over anything it could already have pasted into the
	// conversation, and nothing it could not.
	if err := t.Sandbox.CheckPathRead(path); err != nil {
		return core.ToolResult{}, err
	}
	ref, err := t.Publisher.Publish(ctx, path, a.Name)
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("share: %w", err)
	}
	ref.Caption = a.Caption

	// What the MODEL gets: that the share happened, and the name to call it by
	// in prose. Not the id, not the URL, not the staged path — none of which it
	// needs, and all of which would then ride every subsequent request.
	text := fmt.Sprintf("Shared %s with the user (%s, %d bytes). They can see it in the conversation.", ref.Name, ref.Kind, ref.Size)
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Shared:  []core.SharedFile{ref},
	}, nil
}
