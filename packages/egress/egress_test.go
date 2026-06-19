package egress

import (
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestCheckIPBlocksNonPublic(t *testing.T) {
	g := New()
	blocked := []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"10.1.2.3",        // private
		"172.16.0.1",      // private
		"192.168.1.1",     // private
		"169.254.169.254", // link-local / cloud metadata
		"fd00::1",         // unique-local v6
		"fe80::1",         // link-local v6
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
	}
	for _, s := range blocked {
		if err := g.checkIP(net.ParseIP(s)); err == nil {
			t.Errorf("%s should be blocked", s)
		}
	}
	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, s := range public {
		if err := g.checkIP(net.ParseIP(s)); err != nil {
			t.Errorf("%s should be allowed: %v", s, err)
		}
	}
}

func TestCheckURLScheme(t *testing.T) {
	g := New()
	for _, raw := range []string{"ftp://example.com", "file:///etc/passwd", "gopher://x", "data:text/plain,hi"} {
		if err := g.CheckURL(raw); err == nil {
			t.Errorf("%s should be rejected by scheme", raw)
		}
	}
}

func TestCheckURLLiteralPrivate(t *testing.T) {
	g := New()
	if err := g.CheckURL("http://127.0.0.1:8080/x"); err == nil {
		t.Error("loopback URL should be blocked")
	}
	if err := g.CheckURL("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Error("cloud metadata URL should be blocked")
	}
	if err := g.CheckURL("https://8.8.8.8/"); err != nil {
		t.Errorf("public IP URL should pass: %v", err)
	}
}

func TestControlDefeatsRebinding(t *testing.T) {
	// The Control hook sees the resolved address regardless of hostname,
	// so a name that resolves to a private IP is caught at dial time.
	g := New()
	if err := g.Control("tcp", "127.0.0.1:443", nil); err == nil {
		t.Error("Control should block a loopback dial")
	}
	if err := g.Control("tcp", "10.0.0.5:80", nil); err == nil {
		t.Error("Control should block a private dial")
	}
	if err := g.Control("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("Control should allow a public dial: %v", err)
	}
}

func TestAllowHostBypass(t *testing.T) {
	g := New(AllowHost("localhost"))
	// CheckURL consults the host allowlist before resolving.
	if err := g.CheckURL("http://localhost:11984/search"); err != nil {
		t.Errorf("allowlisted host should pass CheckURL: %v", err)
	}
}

func TestAllowCIDRBypass(t *testing.T) {
	g := New(AllowCIDR("127.0.0.0/8"))
	if err := g.checkIP(net.ParseIP("127.0.0.1")); err != nil {
		t.Errorf("allowlisted CIDR should pass: %v", err)
	}
	if err := g.Control("tcp", "127.0.0.1:80", nil); err != nil {
		t.Errorf("allowlisted CIDR should pass Control: %v", err)
	}
	// A different private IP not in the allowlist is still blocked.
	if err := g.checkIP(net.ParseIP("10.0.0.1")); err == nil {
		t.Error("non-allowlisted private IP should stay blocked")
	}
}

func TestAllowCIDRBareIP(t *testing.T) {
	g := New(AllowCIDR("192.168.1.50"))
	if err := g.checkIP(net.ParseIP("192.168.1.50")); err != nil {
		t.Errorf("bare-IP allowlist should pass: %v", err)
	}
	if err := g.checkIP(net.ParseIP("192.168.1.51")); err == nil {
		t.Error("a neighbor of a bare-IP allowlist entry should stay blocked")
	}
}

func TestAllowAll(t *testing.T) {
	g := New(AllowAll())
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254"} {
		if err := g.checkIP(net.ParseIP(s)); err != nil {
			t.Errorf("AllowAll should permit %s: %v", s, err)
		}
	}
}

func TestParseCIDRErrors(t *testing.T) {
	if _, err := parseCIDR("not-an-ip"); err == nil {
		t.Error("garbage should error")
	}
	if _, err := parseCIDR("10.0.0.0/8"); err != nil {
		t.Errorf("valid CIDR should parse: %v", err)
	}
}

func TestClientRedirectAndCredentials(t *testing.T) {
	// Exercise the CheckRedirect closure directly with IP-literal URLs so
	// the test never touches DNS or the network.
	g := New()
	cr := g.Client(0, 3).CheckRedirect
	if cr == nil {
		t.Fatal("client should set CheckRedirect")
	}
	mk := func(u string) *http.Request {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			t.Fatalf("build req %s: %v", u, err)
		}
		return req
	}

	// Under the cap, redirect to a public host passes.
	if err := cr(mk("https://1.1.1.1/"), []*http.Request{mk("https://8.8.8.8/")}); err != nil {
		t.Errorf("redirect under cap should pass: %v", err)
	}
	// At the cap (3 prior hops) it stops.
	via3 := []*http.Request{mk("https://8.8.8.8/"), mk("https://1.1.1.1/"), mk("https://9.9.9.9/")}
	if err := cr(mk("https://8.8.4.4/"), via3); err == nil {
		t.Error("should stop after max redirects")
	}
	// Redirect to a private address is blocked.
	if err := cr(mk("http://127.0.0.1/"), []*http.Request{mk("https://8.8.8.8/")}); err == nil {
		t.Error("redirect to loopback should be blocked")
	}
	// Authorization is stripped when the redirect crosses hosts.
	cross := mk("https://1.1.1.1/")
	cross.Header.Set("Authorization", "Bearer secret")
	_ = cr(cross, []*http.Request{mk("https://8.8.8.8/")})
	if cross.Header.Get("Authorization") != "" {
		t.Error("Authorization should be stripped across a host change")
	}
	// ...but kept on a same-host redirect.
	same := mk("https://8.8.8.8/page2")
	same.Header.Set("Authorization", "Bearer secret")
	_ = cr(same, []*http.Request{mk("https://8.8.8.8/page1")})
	if same.Header.Get("Authorization") == "" {
		t.Error("Authorization should be kept on a same-host redirect")
	}
}

func TestClientErrorMessagesArePrefixed(t *testing.T) {
	g := New()
	err := g.CheckURL("http://127.0.0.1/")
	if err == nil || !strings.Contains(err.Error(), "egress blocked") {
		t.Errorf("error should be prefixed with 'egress blocked': %v", err)
	}
}
