package extdriver

import (
	"encoding/json"
	"fmt"

	"terva.sh/terva/packages/agent/extproto"
)

// Host-supplied extension config (the manifest's `config` schema). The
// driver is dependency-light — it owns neither config.json nor the schema
// resolution — so the agent layer injects a resolver and the driver only
// delivers the result: the resolved values ride hello_ack at spawn, and a
// later change is pushed to one extension as a config_update event.

// ConfigResolver resolves the host-supplied config for one extension:
// given its name and declared schema, return the values to deliver
// (manifest defaults overlaid with the user's saved config.json values).
// Returning nil/empty means the extension gets no host config. Injected
// by the agent layer, which owns config.json.
type ConfigResolver func(name string, schema []ConfigField) map[string]json.RawMessage

// SetConfigResolver installs the resolver consulted at each spawn to fill
// hello_ack's Config. nil (the default) means extensions receive no host
// config.
func (d *Driver) SetConfigResolver(fn ConfigResolver) {
	d.mu.Lock()
	d.configResolver = fn
	d.mu.Unlock()
}

// resolvedConfigFor returns the resolved config for one extension via the
// injected resolver, or nil when none is set.
func (d *Driver) resolvedConfigFor(name string, schema []ConfigField) map[string]json.RawMessage {
	d.mu.RLock()
	fn := d.configResolver
	d.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(name, schema)
}

// PushConfigUpdate delivers a changed config to ONE running extension as a
// config_update event — targeted, never the all-subscribers fan-out of
// EmitEvent, so one extension's (possibly secret) values never reach
// another. No-op if the extension isn't loaded or didn't subscribe to
// config_update; the value still arrives in hello_ack on its next spawn.
func (d *Driver) PushConfigUpdate(name string, cfg map[string]json.RawMessage) {
	d.mu.RLock()
	ext := d.ext[name]
	d.mu.RUnlock()
	if ext == nil {
		return
	}
	ext.mu.Lock()
	_, subscribed := ext.eventSubs[extproto.EventConfigUpdate]
	ext.mu.Unlock()
	if !subscribed || ext.outbox == nil {
		return
	}
	frame, err := extproto.Encode(extproto.EventFromHost{
		Type:   "event",
		Event:  extproto.EventConfigUpdate,
		Config: cfg,
	})
	if err != nil {
		return
	}
	if err := ext.enqueueFrame(frame, false); err == errOutboxFull {
		fmt.Fprintf(ext.logFile, "[terva] dropped config_update event: outbox full (extension not keeping up)\n")
	}
}
