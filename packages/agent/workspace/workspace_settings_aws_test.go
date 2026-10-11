package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func TestAWSProfileSettingShowsAndWritesUserState(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := providerSession(t, "anthropic")
	if val, ok := itemValue(s, "bedrock_aws_cli_credentials"); !ok || val != "false" {
		t.Fatal("AWS profile setting must show its disabled default")
	}
	for _, val := range []string{"true", "false"} {
		if err := s.settingsAction("set", map[string]string{"key": "bedrock_aws_cli_credentials", "value": val}); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadConfig()
		if err != nil || cfg.Providers["amazon-bedrock"].AWSCLICredentials != (val == "true") {
			t.Fatalf("user setting was not saved: %v", err)
		}
		if got, ok := itemValue(s, "bedrock_aws_cli_credentials"); !ok || got != val {
			t.Fatal("settings interface did not show the saved state")
		}
	}
	if err := s.settingsAction("set", map[string]string{"key": "bedrock_aws_cli_credentials", "value": "maybe"}); err == nil {
		t.Fatal("invalid opt-in value was accepted")
	}
}
