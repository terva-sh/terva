package agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPromptStatusToolHint(t *testing.T) {
	with := BuildSystemPrompt(SystemPromptOpts{CWD: "/x", StatusTool: true})
	if !strings.Contains(with, "terva_status") {
		t.Errorf("expected terva_status hint when StatusTool=true; got:\n%s", with)
	}

	without := BuildSystemPrompt(SystemPromptOpts{CWD: "/x", StatusTool: false})
	if strings.Contains(without, "terva_status") {
		t.Errorf("did not expect terva_status hint when StatusTool=false; got:\n%s", without)
	}
}
