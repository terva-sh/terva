// Package procenv sanitizes the environment handed to long-lived
// child processes — extensions, external connectors, swarm agents,
// and (future) MCP servers.
//
// Threat model: a hostile or compromised value in the *inherited*
// environment can hijack any dynamically-linked or interpreted child
// at load time (LD_PRELOAD, DYLD_INSERT_LIBRARIES, PYTHONPATH,
// NODE_OPTIONS, …) before the child runs a single line of its own
// code. Tool-level permission checks never see it. Stripping these
// keys at every spawn point is cheap defense-in-depth; an extension
// that legitimately needs one can set it explicitly for its own
// children.
//
// This deliberately does NOT apply to the bash tool: that runs the
// user's own commands, which are entitled to the user's environment.
package procenv

import (
	"os"
	"strings"
)

// Inherited returns the current process environment with disallowed
// keys removed — the right base env for spawning a managed child.
func Inherited() []string {
	return Sanitize(os.Environ())
}

// disallowedExact lists environment keys stripped from the inherited
// environment before spawning a managed child. Grouped by what the
// key can inject. Comparison is case-insensitive because Windows env
// keys are.
var disallowedExact = map[string]struct{}{
	// Python: import-path and startup-code injection.
	"PYTHONPATH":    {},
	"PYTHONSTARTUP": {},
	"PYTHONHOME":    {},
	// Node: V8/runtime flags and module-resolution injection.
	"NODE_OPTIONS": {},
	"NODE_PATH":    {},
	// Ruby: interpreter options and load-path injection.
	"RUBYOPT": {},
	"RUBYLIB": {},
	// RubyGems: gem resolution redirect.
	"GEM_PATH": {},
	"GEM_HOME": {},
	// Perl: load-path and option injection.
	"PERL5LIB": {},
	"PERL5OPT": {},
	// JVM: classpath and agent injection (-javaagent via options).
	"CLASSPATH":         {},
	"JAVA_TOOL_OPTIONS": {},
	"_JAVA_OPTIONS":     {},
	"JDK_JAVA_OPTIONS":  {},
	// .NET: startup hook and profiler injection.
	"DOTNET_STARTUP_HOOKS":     {},
	"CORECLR_PROFILER":         {},
	"CORECLR_PROFILER_PATH":    {},
	"CORECLR_ENABLE_PROFILING": {},
	// POSIX shells: startup-file injection for non-interactive shells
	// a child might exec.
	"BASH_ENV": {},
	"ENV":      {},
	"ZDOTDIR":  {},
}

// disallowedPrefixes catches whole families: the ELF loader (LD_*)
// and the Mach-O loader (DYLD_*) read many keys, all of which only
// make sense for deliberate debugging — never for an inherited
// environment crossing a trust boundary.
var disallowedPrefixes = []string{"LD_", "DYLD_"}

// Disallowed reports whether key is stripped by Sanitize.
func Disallowed(key string) bool {
	k := strings.ToUpper(key)
	if _, ok := disallowedExact[k]; ok {
		return true
	}
	for _, p := range disallowedPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// Sanitize returns a copy of env (KEY=VALUE strings, as from
// os.Environ) with disallowed keys removed. Malformed entries with
// no '=' are dropped too — nothing legitimate produces them.
func Sanitize(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if Disallowed(kv[:eq]) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
