package tools

// HostRouted is implemented by dispatch tools that inherit the host
// session's provider/model to route (and tier-resolve) their sub-agents.
// The workspace refreshes it after a mid-session model swap (switchModel)
// so a sub-agent spawned afterward follows the CURRENT route, not the
// pre-swap one.
//
// SetHost must be safe to call while a turn is running — a swap can land
// concurrently with a spawn — mirroring StatusTool.SetProvider. Implemented
// by swarm_spawn, actor_spawn, and raati_convene (each with an RWMutex over
// its host fields); switchModel refreshes all of them generically.
type HostRouted interface {
	SetHost(provider, model string)
}

// Every dispatch tool that inherits the host route must stay HostRouted so
// switchModel keeps refreshing it after a swap.
var (
	_ HostRouted = (*SwarmSpawnTool)(nil)
	_ HostRouted = (*ActorSpawnTool)(nil)
	_ HostRouted = (*RaatiConveneTool)(nil)
)
