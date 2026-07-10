package agent

import (
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/tui"
)

// experienceTheme returns a copy of th with non-coding spinner + greeting
// flavor for the chat/play meta-modes, so a roleplay or a conversation doesn't
// say "arguing with a stack trace" while it thinks. A normal session is
// returned unchanged. Theme is a value type and we only replace slice headers,
// so the caller's theme is not mutated.
func experienceTheme(th tui.Theme, experience string) tui.Theme {
	switch experience {
	case build.ExperienceChat:
		th.SpinnerMessages = experienceSpinnerMessages
		th.Greetings = chatGreetings
	case build.ExperiencePlay:
		th.SpinnerMessages = experienceSpinnerMessages
		th.Greetings = playGreetings
	}
	return th
}

// experienceSpinnerMessages are calm, domain-neutral working lines (no code
// references, no {verb}/{noun} templating) shared by chat and play.
var experienceSpinnerMessages = []string{
	"thinking",
	"considering",
	"reflecting",
	"musing",
	"turning it over",
	"letting it settle",
	"picturing it",
	"weighing it",
	"gathering the thread",
	"taking it in",
}

var chatGreetings = []string{
	"what's on your mind?",
	"i'm listening.",
	"tell me anything.",
	"where shall we begin?",
	"say the word.",
}

var playGreetings = []string{
	"what do you see?",
	"where to?",
	"the world is waiting.",
	"your move.",
	"let's begin.",
}
