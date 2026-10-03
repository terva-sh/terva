package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// TalkootWorkspaceStatus is where a movable Talkoot member works now.
type TalkootWorkspaceStatus struct {
	// InWorktree is true in the member's own worktree, and false in the
	// talkoot's home checkout.
	InWorktree bool
	Dir        string
}

// TalkootMover moves a member with workspace: either between its own
// worktree and the home checkout. The workspace binds it to one seat, so the
// model never names the member.
type TalkootMover interface {
	Move(ctx context.Context, action string) (TalkootWorkspaceStatus, error)
}

const talkootWorkspaceDesc = "Move between your own worktree and the home checkout of your talkoot. Your conversation stays, and the move applies to your next tool call.\n\nWith action enter, the tool moves you into your worktree. There your posture permits you to change files. To work on a branch, run git switch <branch> in your worktree. If git refuses because another checkout holds the branch, ask its owner to switch away.\n\nWith action leave, the tool moves you back to the home checkout, where you can only read. Commit your work first. Then run git switch --detach, so another member can check out your branch. Your worktree stays yours, and a later enter returns you to it.\n\nWith action status, the tool tells you where you work."

const talkootWorkspaceSchema = `{"type":"object","properties":{"action":{"type":"string","enum":["enter","leave","status"],"description":"enter moves you into your worktree. leave moves you back to the home checkout. status tells you where you work."}},"required":["action"]}`

// TalkootWorkspaceTool moves a movable member into or out of its worktree.
type TalkootWorkspaceTool struct{ Mover TalkootMover }

func (t *TalkootWorkspaceTool) Name() string { return "talkoot_workspace" }
func (t *TalkootWorkspaceTool) Description() string {
	return i18n.D("tool.talkoot_workspace.description", talkootWorkspaceDesc)
}
func (t *TalkootWorkspaceTool) Schema() json.RawMessage {
	return json.RawMessage(talkootWorkspaceSchema)
}
func (t *TalkootWorkspaceTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if t.Mover == nil {
		return core.ToolResult{}, errors.New("this session is not a talkoot member that can move")
	}
	var a struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	switch a.Action {
	case "enter", "leave", "status":
	default:
		return core.ToolResult{}, fmt.Errorf("action %q is not enter, leave, or status", a.Action)
	}
	st, err := t.Mover.Move(ctx, a.Action)
	if err != nil {
		return core.ToolResult{}, err
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: talkootWorkspaceText(st)}}}, nil
}

func talkootWorkspaceText(st TalkootWorkspaceStatus) string {
	if st.InWorktree {
		return fmt.Sprintf("You work in your worktree %s. Your posture applies here. To work on a branch, run git switch <branch>.", st.Dir)
	}
	return fmt.Sprintf("You work in the home checkout %s. You can only read here. To change files, enter your worktree with talkoot_workspace.", st.Dir)
}
