package procenv

import (
	"reflect"
	"testing"
)

func TestSanitizeStripsInjectionKeys(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp",
		"LD_AUDIT=/tmp/a.so",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib",
		"DYLD_FALLBACK_LIBRARY_PATH=/tmp",
		"PYTHONPATH=/tmp/pwn",
		"PYTHONSTARTUP=/tmp/pwn.py",
		"NODE_OPTIONS=--require /tmp/evil.js",
		"RUBYOPT=-r/tmp/evil",
		"GEM_HOME=/tmp/gems",
		"PERL5OPT=-M/tmp",
		"CLASSPATH=/tmp/evil.jar",
		"JAVA_TOOL_OPTIONS=-javaagent:/tmp/evil.jar",
		"_JAVA_OPTIONS=-javaagent:/tmp/evil.jar",
		"DOTNET_STARTUP_HOOKS=/tmp/evil.dll",
		"CORECLR_PROFILER={...}",
		"BASH_ENV=/tmp/evil.sh",
		"ENV=/tmp/evil.sh",
		"ZDOTDIR=/tmp",
		"TERVA_HOME=/home/u/.terva",
		"ANTHROPIC_API_KEY=sk-test",
	}
	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"TERVA_HOME=/home/u/.terva",
		"ANTHROPIC_API_KEY=sk-test",
	}
	got := Sanitize(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sanitize() = %v, want %v", got, want)
	}
}

func TestSanitizeIsCaseInsensitive(t *testing.T) {
	// Windows env keys are case-insensitive, so the filter must be:
	// a child spawned with ld_preload= set would still be hijacked on
	// a case-preserving platform.
	in := []string{"ld_preload=/tmp/x", "Pythonpath=/tmp", "Node_Options=--x", "Path=C:\\Windows"}
	want := []string{"Path=C:\\Windows"}
	if got := Sanitize(in); !reflect.DeepEqual(got, want) {
		t.Errorf("Sanitize() = %v, want %v", got, want)
	}
}

func TestSanitizeDropsMalformedEntries(t *testing.T) {
	in := []string{"NOEQUALS", "=novalue", "OK=1"}
	want := []string{"OK=1"}
	if got := Sanitize(in); !reflect.DeepEqual(got, want) {
		t.Errorf("Sanitize() = %v, want %v", got, want)
	}
}

func TestSanitizeDoesNotMutateInput(t *testing.T) {
	in := []string{"A=1", "LD_PRELOAD=x", "B=2"}
	orig := append([]string{}, in...)
	_ = Sanitize(in)
	if !reflect.DeepEqual(in, orig) {
		t.Errorf("input mutated: %v", in)
	}
}

func TestDisallowed(t *testing.T) {
	cases := map[string]bool{
		"LD_PRELOAD":              true,
		"DYLD_ANYTHING_AT_ALL":    true,
		"PYTHONPATH":              true,
		"pythonpath":              true,
		"PATH":                    false, // children need PATH; only manifest *overrides* of it would be a problem
		"HOME":                    false,
		"TERVA_SWARM_AGENT_ID":    false,
		"GOFLAGS":                 false, // deliberate: stripping it breaks legitimate Go dev workflows
		"PYTHONDONTWRITEBYTECODE": false,
	}
	for k, want := range cases {
		if got := Disallowed(k); got != want {
			t.Errorf("Disallowed(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestInheritedStripsLivePollution(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	t.Setenv("TERVA_PROCENV_CANARY", "ok")
	for _, kv := range Inherited() {
		if kv == "LD_PRELOAD=/tmp/evil.so" {
			t.Fatal("Inherited() leaked LD_PRELOAD")
		}
	}
	found := false
	for _, kv := range Inherited() {
		if kv == "TERVA_PROCENV_CANARY=ok" {
			found = true
		}
	}
	if !found {
		t.Fatal("Inherited() dropped a benign key")
	}
}
