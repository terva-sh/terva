package lazytools

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

// The note that tells the model which tool groups are hidden is model-facing
// text. Two agents in one process, prompting at once, each send it in their
// own language: the component renders through the agent it is bound to.
func TestEachAgentsNoteIsInItsOwnLanguage(t *testing.T) {
	clients := map[string]*reqCaptureClient{"fi": {}, "sv": {}}
	var wg sync.WaitGroup
	errs := make(chan error, len(clients))
	for lang, client := range clients {
		v := New()
		a, err := core.New(client, "m",
			core.WithAssembler(&frame{system: "sys", v: v}),
			core.WithTools(decayRegistry()),
			core.WithGate(core.AllowAll),
			core.WithComponent(v),
			core.WithTranslator(tagged(lang)))
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.Run(context.Background(), core.PromptInput{Text: "go"}, nil)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for lang, client := range clients {
		client.mu.Lock()
		tail := strings.Join(client.ephemeral, "\n")
		client.mu.Unlock()
		if !strings.Contains(tail, "mail_send") {
			t.Fatalf("the %s agent sent no inactive-group note: %q", lang, tail)
		}
		for other := range clients {
			tag := "[" + other + "] "
			if has := strings.Contains(tail, tag); has != (other == lang) {
				t.Errorf("the %s agent's tail contains %q: %v, want %v\n%s", lang, tag, has, other == lang, tail)
			}
		}
	}
}
