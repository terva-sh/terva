package config

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestAWSCLIProfileUserOnlyOptIn(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	writeProjectConfig(t, cwd, `{"disable_extensions":["positive-control"],"providers":{"amazon-bedrock":{"aws_cli_credentials":true}}}`)
	for _, trusted := range []bool{false, true} {
		eff := ResolveConfig(cwd, trusted)
		if len(eff.DisableExtensions) != 1 || eff.DisableExtensions[0] != "positive-control" {
			t.Fatal("project fixture did not reach the resolver")
		}
		if eff.Providers["amazon-bedrock"].AWSCLICredentials || AWSCLIProfileEnabled() {
			t.Fatal("project config granted AWS credential helper execution")
		}
	}
	if err := SaveConfig(Config{Providers: map[string]ProviderSettings{"amazon-bedrock": {AWSCLICredentials: true}}}); err != nil {
		t.Fatal(err)
	}
	if !ResolveConfig(cwd, true).Providers["amazon-bedrock"].AWSCLICredentials || !AWSCLIProfileEnabled() {
		t.Fatal("user opt-in did not reach the resolver")
	}
}
