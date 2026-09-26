package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/core/i18n"
)

// tagged is a translator that marks everything it renders with its language.
type tagged string

func (l tagged) T(source string, args ...any) string {
	return "[" + string(l) + "] " + fmt.Sprintf(source, args...)
}
func (l tagged) P(key, english string, args ...any) string {
	return "[" + string(l) + "] " + fmt.Sprintf(english, args...)
}

// maxStepsRun runs one prompt on a fresh agent that stops after a single step,
// and returns the error the engine renders for it. It calls no t method, so a
// goroutine may use it.
func maxStepsRun(opts ...Option) (error, error) {
	tool := &cancelProbeTool{}
	a, err := New(&stepClient{}, "m", append([]Option{WithGate(AllowAll),
		WithTools(Registry{tool.Name(): tool}), WithMaxSteps(1)}, opts...)...)
	if err != nil {
		return nil, err
	}
	err = a.Run(context.Background(), PromptInput{Text: "go"}, nil)
	if err == nil || !strings.Contains(err.Error(), "max steps") {
		return nil, fmt.Errorf("the run ended with %v; want the max-steps error", err)
	}
	return err, nil
}

func maxStepsError(t *testing.T, opts ...Option) error {
	t.Helper()
	runErr, err := maxStepsRun(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return runErr
}

// Two agents in one process, running at once, each render the engine's text
// through their own translator.
func TestTwoAgentsRenderInTheirOwnLanguages(t *testing.T) {
	var wg sync.WaitGroup
	got := map[string]string{}
	var mu sync.Mutex
	for _, lang := range []string{"fi", "sv"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runErr, err := maxStepsRun(WithTranslator(tagged(lang)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				got[lang] = err.Error()
				return
			}
			got[lang] = runErr.Error()
		}()
	}
	wg.Wait()
	for lang, msg := range got {
		if !strings.HasPrefix(msg, "["+lang+"] ") {
			t.Errorf("the %s agent rendered %q; want its own translation", lang, msg)
		}
	}
}

// An agent given no translator renders through the process-wide one, as every
// agent did before WithTranslator existed, and follows it when it changes.
func TestAnAgentWithoutATranslatorUsesTheProcessWideOne(t *testing.T) {
	t.Cleanup(func() { i18n.Use(nil) })
	if msg := maxStepsError(t).Error(); strings.HasPrefix(msg, "[") {
		t.Errorf("with no translator anywhere the error is %q; want English", msg)
	}
	i18n.Use(tagged("de"))
	for name, opts := range map[string][]Option{"no option": nil, "WithTranslator(nil)": {WithTranslator(nil)}} {
		if msg := maxStepsError(t, opts...).Error(); !strings.HasPrefix(msg, "[de] ") {
			t.Errorf("%s: the error is %q; want the process-wide translation", name, msg)
		}
	}
	if msg := maxStepsError(t, WithTranslator(tagged("fi"))).Error(); !strings.HasPrefix(msg, "[fi] ") {
		t.Errorf("an agent's own translator lost to the process-wide one: %q", msg)
	}
}

// A typed nil would panic on the first string; New refuses it instead.
func TestWithTranslatorRefusesATypedNil(t *testing.T) {
	var p *tagged
	if _, err := New(nil, "m", WithGate(AllowAll), WithTranslator(p)); err == nil || !strings.Contains(err.Error(), "typed-nil") {
		t.Errorf("a typed-nil translator gave %v; want New's refusal", err)
	}
}
