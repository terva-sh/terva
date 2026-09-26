package stall

import (
	"context"
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

// The refusal a spinning call gets back is model-facing text, and it is in the
// language of the agent the detector is bound to.
func TestTheRefusalIsInTheAgentsLanguage(t *testing.T) {
	d := on()
	a, err := core.New(nil, "m", core.WithGate(core.AllowAll), core.WithComponent(d),
		core.WithTranslator(tagged("fi")))
	if err != nil {
		t.Fatal(err)
	}
	noopUpdate(&d.t, stallRefuseAt)
	ok, reason, _ := a.Gate().CheckTool(context.Background(), updateCall(), nil)
	if ok {
		t.Fatal("precondition: the call should have been refused")
	}
	if !strings.HasPrefix(reason, "[fi] ") {
		t.Errorf("the refusal is %q; want it in the agent's language", reason)
	}
}

// The translator sits beside the tracker's per-turn state, so a reset at a
// prompt's start and a forgive after a declined escalation must both leave
// it alone. A later rewrite that zeroes the tracker would otherwise switch
// the agent's notes back to the process-wide language without a sound.
func TestTheLanguageOutlivesAResetAndAForgive(t *testing.T) {
	d := on()
	a, err := core.New(nil, "m", core.WithGate(core.AllowAll), core.WithComponent(d),
		core.WithTranslator(tagged("fi")))
	if err != nil {
		t.Fatal(err)
	}
	d.stepGate().Begin()
	d.mu.Lock()
	d.t.forgive()
	d.mu.Unlock()
	noopUpdate(&d.t, stallRefuseAt)
	_, reason, _ := a.Gate().CheckTool(context.Background(), updateCall(), nil)
	if !strings.HasPrefix(reason, "[fi] ") {
		t.Errorf("after a reset and a forgive the refusal is %q; want it still in the agent's language", reason)
	}
}
