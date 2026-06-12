package chat

import (
	"sort"
	"sync"
)

// Service describes one registered chat service: how to tell whether
// it's configured, how to build its connector from persisted state,
// and its CLI lifecycle hooks. Connector packages register themselves
// (telegram does so from an init; phase 3 of the connector plan puts
// that registration behind a build tag so binaries only pay for the
// services they compile in).
type Service struct {
	Name string

	// Configured reports whether persisted credentials exist under
	// tervaHome (e.g. a bot token in bot.json).
	Configured func(tervaHome string) bool

	// NewConnector builds the transport and its pairing state from
	// persisted config. warn receives transport log lines (may be
	// nil for default stderr logging).
	NewConnector func(tervaHome string, warn func(string)) (Connector, Pairing, error)

	// Setup interactively provisions credentials (`terva bot setup`).
	Setup func(tervaHome string) error

	// StatusText renders the `terva bot status` config block (tokens
	// masked). Daemon liveness is reported separately by the caller.
	StatusText func(tervaHome string) (string, error)

	// Reset removes persisted credentials (`terva bot reset`).
	// Returns the path it removed, "" if nothing existed.
	Reset func(tervaHome string) (string, error)

	// Dev marks a connector loaded via --connector-manifest for this
	// invocation only — never discovered, never persisted. Status
	// surfaces tag it "(dev)" so it is never mistaken for an
	// installed connector.
	Dev bool
}

var (
	registryMu sync.Mutex
	registry   = map[string]Service{}
)

// Register adds a service. Last registration for a name wins.
func Register(s Service) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[s.Name] = s
}

// Services returns all registered services, sorted by name.
func Services() []Service {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]Service, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup finds a service by name.
func Lookup(name string) (Service, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	s, ok := registry[name]
	return s, ok
}

// DefaultServiceName picks the service used when --connector isn't
// given: a dev connector when one was loaded (passing
// --connector-manifest is explicit intent for this invocation, so it
// outranks compiled-in services), otherwise the sole registered
// service, the historical default (telegram) when several are
// compiled in, or "" when the binary was built with every connector
// tagged out.
func DefaultServiceName() string {
	svcs := Services()
	for _, s := range svcs {
		if s.Dev {
			return s.Name
		}
	}
	switch len(svcs) {
	case 0:
		return ""
	case 1:
		return svcs[0].Name
	}
	if _, ok := Lookup("telegram"); ok {
		return "telegram"
	}
	return svcs[0].Name
}
