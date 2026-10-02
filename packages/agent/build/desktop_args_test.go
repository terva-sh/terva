package build

import (
	"terva.sh/terva/packages/agent/mode"
	"testing"
)

func TestDesktopArguments(t *testing.T) {
	for _, conflicting := range [][]string{{"--print"}, {"--json"}, {"--rpc"}, {"--acp"}, {"--member"}, {"--serve"}, {"--attach"}, {"--replay", "fixture.jsonl"}, {"--swarm-agent", "fixture"}} {
		for _, flags := range [][]string{append([]string{"--desktop"}, conflicting...), append(append([]string{}, conflicting...), "--desktop"), append(append([]string{}, conflicting...), "--web", "--desktop")} {
			if _, err := ParseArgs(flags); err == nil {
				t.Fatalf("accepted conflicting modes %v", flags)
			}
		}
	}
	for _, flags := range [][]string{{"--desktop"}, {"--desktop", "--desktop-port", "9134"}} {
		a, err := ParseArgs(flags)
		if err != nil {
			t.Fatal(err)
		}
		if !a.Desktop || a.Mode != mode.Web {
			t.Fatalf("wrong mode: %+v", a)
		}
		if len(flags) == 1 && a.DesktopPort != 0 {
			t.Fatal("default must request a random port")
		}
		if len(flags) > 1 && a.DesktopPort != 9134 {
			t.Fatal("explicit port lost")
		}
	}
	for _, flags := range [][]string{
		{"--desktop-port", "9134"}, {"--desktop", "--desktop-port", "0"},
		{"--desktop", "--desktop-port", "65536"}, {"--desktop", "--desktop-port", "abc"},
		{"--desktop", "--web-addr", "0.0.0.0:9"}, {"--desktop", "--web-token", "secret"},
		{"--desktop", "--web-insecure"}, {"--desktop", "--web-auth-header", "X-User"},
		{"--desktop", "--web-allow-restart"}, {"--desktop", "--rpc"},
	} {
		if _, err := ParseArgs(flags); err == nil {
			t.Fatalf("accepted %v", flags)
		}
	}
}
