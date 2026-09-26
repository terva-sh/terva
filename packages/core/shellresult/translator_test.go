package shellresult

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// tagged is a translator that marks everything it renders with its language.
type tagged string

func (l tagged) T(source string, args ...any) string {
	return "[" + string(l) + "] " + fmt.Sprintf(source, args...)
}
func (l tagged) P(key, english string, args ...any) string {
	return "[" + string(l) + "] " + fmt.Sprintf(english, args...)
}

// The shell result's framing is model-facing text, and it is in the language
// of the agent the slot is attached to.
func TestTheBlockIsInTheAgentsLanguage(t *testing.T) {
	s := &Slot{}
	s.SetEnabled(true)
	if _, err := core.New(nil, "m", core.WithGate(core.AllowAll), core.WithTranslator(tagged("fi")), core.WithComponent(s)); err != nil {
		t.Fatal(err)
	}
	s.Set("git status", "clean")
	if got := s.Segment().Content; !strings.Contains(got, "[fi] ") {
		t.Errorf("the block is %q; want its framing in the agent's language", got)
	}
}
