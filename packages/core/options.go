package core

import (
	"errors"
	"fmt"
	"time"

	"terva.sh/terva/packages/core/i18n"
	"terva.sh/terva/packages/provider"
)

// ErrNilGate is New's answer when no usable gate is given, in any spelling of
// nil. There is no default: an engine that allowed every tool call unless told
// otherwise would fail open. Pass core.AllowAll to allow every call on purpose.
var ErrNilGate = errors.New("gate is nil; pass a Gate, or core.AllowAll to allow every tool call")

// Option configures an agent built by New. Options apply in order, and a later
// option of the same kind replaces an earlier one, except WithComponent,
// which adds.
type Option struct {
	apply func(*settings) error
}

// settings is what the options set. A pointer or a set flag marks a value
// the host chose, so New can tell it from the engine's default.
type settings struct {
	assembler  ContextAssembler
	tools      Registry
	gate       Gate
	components []any

	catalog    provider.ModelCatalog
	store      TranscriptStore
	asker      Asker
	compaction CompactionPolicy

	maxSteps, maxTokens *int
	maxRetries          *int
	retryBase           *time.Duration

	reasoning        *string
	reasoningSummary *string
	showReasoning    *bool
	temperature      *float32
	imageOutput      *provider.ImageOutputConfig

	translator i18n.Translator
}

// New builds an agent for client and model. WithGate is required; everything
// else has a default. It refuses a configuration it cannot build rather than
// returning an agent that fails on its first turn.
//
// New is the constructor to extend: a new setting is a new Option, and a new
// hook is a capability interface a component implements (see WithComponent),
// so neither changes this signature.
func New(client provider.Client, model string, opts ...Option) (*Agent, error) {
	var s settings
	for _, o := range opts {
		if o.apply == nil {
			return nil, errors.New("core: New: the zero Option; build options with the With functions")
		}
		if err := o.apply(&s); err != nil {
			return nil, fmt.Errorf("core: New: %w", err)
		}
	}
	if isNilGate(s.gate) {
		return nil, fmt.Errorf("core: New: %w", ErrNilGate)
	}
	if err := checkComponents(s.components); err != nil {
		return nil, fmt.Errorf("core: New: %w", err)
	}
	gate, err := wrapGate(s.gate, s.components)
	if err != nil {
		return nil, fmt.Errorf("core: New: %w", err)
	}

	a := newAgent(client, model, s.assembler, s.tools, gate)
	// The catalog first: SetCatalog re-derives MaxTokens from the model's cap,
	// and a WithMaxTokens the host passed must win over that, not lose to it.
	if s.catalog != nil {
		a.SetCatalog(s.catalog)
	}
	if s.maxSteps != nil {
		a.maxSteps = *s.maxSteps
	}
	if s.maxTokens != nil {
		a.maxTokens = *s.maxTokens
	}
	if s.maxRetries != nil {
		a.maxRetries = *s.maxRetries
	}
	if s.retryBase != nil {
		a.retryBaseDelay = *s.retryBase
	}
	if s.reasoning != nil {
		a.reasoning, a.reasoningSet = *s.reasoning, true
	}
	if s.reasoningSummary != nil {
		a.reasoningSummary = *s.reasoningSummary
	}
	if s.showReasoning != nil {
		a.showReasoning = *s.showReasoning
	}
	if s.temperature != nil {
		t := *s.temperature
		a.temperature = &t
	}
	if s.imageOutput != nil {
		a.imageOutput = s.imageOutput
	}
	if s.asker != nil {
		a.asker = s.asker
	}
	if s.compaction != nil {
		a.compactionPolicy = s.compaction
	}
	a.translator = s.translator
	a.connect(s.components)
	// The store last, after the components' observers, so it sees the agent
	// as the host configured it.
	if s.store != nil {
		a.AttachTranscriptStore(s.store)
	}
	return a, nil
}

func option(f func(*settings) error) Option { return Option{apply: f} }

// WithGate sets the permission gate every tool call passes. It is required.
func WithGate(g Gate) Option {
	return option(func(s *settings) error { s.gate = g; return nil })
}

// WithAssembler sets the ContextAssembler each request's frame is built from.
// Without it the frame is empty.
func WithAssembler(asm ContextAssembler) Option {
	return option(func(s *settings) error { s.assembler = asm; return nil })
}

// WithTools sets the tool registry.
func WithTools(tools Registry) Option {
	return option(func(s *settings) error { s.tools = tools; return nil })
}

// WithComponent attaches c through every capability interface it implements:
// Binder, StepGater, ContinuationGater, GateWrapper, EventObserver,
// MessageObserver, DispatchWatcher, QueueObserver, TurnFilter, MessageFilter
// and ToolVisibility. Components attach in the order they are passed.
func WithComponent(c any) Option {
	return option(func(s *settings) error {
		if c == nil {
			return errNilComponent
		}
		s.components = append(s.components, c)
		return nil
	})
}

// WithCatalog sets the model catalog, as SetCatalog does. Without it the
// agent knows the built-in models.
func WithCatalog(c provider.ModelCatalog) Option {
	return option(func(s *settings) error { s.catalog = c; return nil })
}

// WithTranscriptStore attaches store, as AttachTranscriptStore does.
func WithTranscriptStore(store TranscriptStore) Option {
	return option(func(s *settings) error { s.store = store; return nil })
}

// WithAsker sets the Asker a tool's structured questions go to.
func WithAsker(ask Asker) Option {
	return option(func(s *settings) error { s.asker = ask; return nil })
}

// WithCompactionPolicy sets the compaction policy. Without it the agent uses
// DefaultCompactionPolicy.
func WithCompactionPolicy(p CompactionPolicy) Option {
	return option(func(s *settings) error { s.compaction = p; return nil })
}

// WithMaxSteps bounds the steps of one prompt. 0 is unlimited, the default.
func WithMaxSteps(n int) Option {
	return option(func(s *settings) error {
		if n < 0 {
			return fmt.Errorf("WithMaxSteps: %d is negative", n)
		}
		s.maxSteps = &n
		return nil
	})
}

// WithMaxTokens caps each response's output tokens. 0 leaves it to the model.
func WithMaxTokens(n int) Option {
	return option(func(s *settings) error {
		if n < 0 {
			return fmt.Errorf("WithMaxTokens: %d is negative", n)
		}
		s.maxTokens = &n
		return nil
	})
}

// WithRetries sets how often a failed request is retried and the first delay,
// which doubles up to 60 seconds. The default is 6 retries from 2 seconds.
func WithRetries(max int, base time.Duration) Option {
	return option(func(s *settings) error {
		if max < 0 || base < 0 {
			return fmt.Errorf("WithRetries: %d retries from %v; neither may be negative", max, base)
		}
		s.maxRetries, s.retryBase = &max, &base
		return nil
	})
}

// WithReasoning sets the reasoning level as an explicit choice, as
// SetReasoning does: it wins over the model's default, and "" means off.
func WithReasoning(level string) Option {
	return option(func(s *settings) error { s.reasoning = &level; return nil })
}

// WithReasoningSummary sets the reasoning summary mode.
func WithReasoningSummary(mode string) Option {
	return option(func(s *settings) error { s.reasoningSummary = &mode; return nil })
}

// WithShowReasoning sets whether reasoning is streamed as events.
func WithShowReasoning(show bool) Option {
	return option(func(s *settings) error { s.showReasoning = &show; return nil })
}

// WithTemperature sets the sampling temperature. Without it the model's
// default applies.
func WithTemperature(t float32) Option {
	return option(func(s *settings) error { s.temperature = &t; return nil })
}

// WithImageOutput sets the image output configuration.
func WithImageOutput(cfg *provider.ImageOutputConfig) Option {
	return option(func(s *settings) error { s.imageOutput = cfg; return nil })
}

// WithTranslator gives the agent its own translator for the text it renders:
// the model-facing prompts and notes, and the messages its turns report.
// Without it, or with nil, the agent renders through the process-wide
// translator that i18n.Use installs, as before. Two agents in one process can
// therefore speak different languages. Text a host renders through core's
// helper functions, such as ClassifyRecoverable, stays process-wide.
//
// Unstable: it takes an i18n.Translator, and packages/core/i18n carries no
// promise before 1.0.
func WithTranslator(tr i18n.Translator) Option {
	return option(func(s *settings) error {
		if tr != nil && isNil(tr) {
			return fmt.Errorf("WithTranslator: a typed-nil %T; pass nil for the process-wide translator", tr)
		}
		s.translator = tr
		return nil
	})
}
