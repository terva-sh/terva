package agent

import (
	"testing"

	"terva.sh/terva/packages/agent/build"
)

func TestNormalizeAttachURL(t *testing.T) {
	cases := []struct {
		in, want string
		wantUnix string
		wantErr  bool
	}{
		{in: "", want: "ws://127.0.0.1:8730/ws"},
		{in: "192.168.1.5:8730", want: "ws://192.168.1.5:8730/ws"},
		{in: "daemon.local:9000", want: "ws://daemon.local:9000/ws"},
		{in: "ws://host:1234", want: "ws://host:1234/ws"},
		{in: "ws://host:1234/", want: "ws://host:1234/ws"},
		{in: "wss://host/custom-path", want: "wss://host/custom-path"},
		{in: "http://host:8730", want: "ws://host:8730/ws"},
		{in: "https://host", want: "wss://host/ws"},
		{in: "ftp://host", wantErr: true},
		// A daemon serving a filesystem socket (--web-addr unix:/path or a
		// systemd socket unit). Both the bare and URL-ish spellings resolve
		// to the same path; the ws URL is the upgrade's placeholder host.
		{in: "unix:/run/terva.sock", want: "ws://unix/ws", wantUnix: "/run/terva.sock"},
		{in: "unix:///run/terva.sock", want: "ws://unix/ws", wantUnix: "/run/terva.sock"},
		{in: "unix:", wantErr: true},
	}
	for _, c := range cases {
		got, err := normalizeAttachURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeAttachURL(%q) = %+v, want error", c.in, got)
			}
			continue
		}
		if err != nil || got.URL != c.want || got.UnixPath != c.wantUnix {
			t.Errorf("normalizeAttachURL(%q) = %+v, %v; want URL %q unix %q", c.in, got, err, c.want, c.wantUnix)
		}
	}
}

func TestParseAttachArgs(t *testing.T) {
	a, err := build.ParseArgs([]string{"--attach"})
	if err != nil || a.Mode != build.ModeAttach || a.AttachURL != "" {
		t.Fatalf("--attach: %+v, %v", a, err)
	}
	a, err = build.ParseArgs([]string{"--attach", "host:8730", "--token", "sekrit"})
	if err != nil || a.Mode != build.ModeAttach || a.AttachURL != "host:8730" || a.Token != "sekrit" {
		t.Fatalf("--attach with URL+token: %+v, %v", a, err)
	}
	// The optional URL must not swallow a following flag.
	a, err = build.ParseArgs([]string{"--attach", "--token", "x"})
	if err != nil || a.AttachURL != "" || a.Token != "x" {
		t.Fatalf("--attach then flag: %+v, %v", a, err)
	}
}
