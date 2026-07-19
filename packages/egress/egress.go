// Package egress is terva's shared outbound-network safety guard: the
// SSRF / private-network defense intended as the single chokepoint for
// network-touching features terva itself drives. The first consumer is
// Stage's card URL import (packages/agent/workspace.fetchCardBytes), which
// exercises this package end-to-end so it cannot silently go dead; it was
// staged ahead of that for the MCP HTTP transport
// (docs/plans/mcp-http-transport.md) and host-side web policy. The rules
// live here, tested, instead of being re-derived per tool.
//
// The guard blocks connections to non-public addresses — loopback,
// RFC1918 private ranges, link-local (including the 169.254.169.254 cloud
// metadata endpoint), unique-local IPv6, multicast, and the unspecified
// address — unless the caller explicitly allowlists a host or CIDR. It
// enforces at DIAL time via a net.Dialer.Control hook, which sees the
// actual IP being connected to and therefore defeats DNS rebinding (a
// hostname that passes a resolve-time check but resolves to a private IP
// at connect time). CheckURL is a fast, clear-error pre-check on top.
package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Guard decides whether an outbound connection is permitted. The zero
// value is not usable; construct with New. Safe for concurrent use after
// construction (it is read-only).
type Guard struct {
	allowHosts map[string]bool // explicit hostname allowlist, lowercased
	allowNets  []*net.IPNet    // explicit IP/CIDR allowlist
	allowAll   bool            // escape hatch: disable all blocking
}

// Option configures a Guard.
type Option func(*Guard)

// AllowHost permits a specific hostname (case-insensitive) even if it
// resolves to an otherwise-blocked address. Use for an intentional local
// service (e.g. a self-hosted search instance on localhost).
func AllowHost(host string) Option {
	return func(g *Guard) { g.allowHosts[strings.ToLower(strings.TrimSpace(host))] = true }
}

// AllowCIDR permits an IP or CIDR range. A bare IP is treated as a /32
// (or /128). Invalid input is ignored so a bad config line can't crash
// the guard; pair with ParseCIDR if you need to surface the error.
func AllowCIDR(cidr string) Option {
	return func(g *Guard) {
		if n, err := parseCIDR(cidr); err == nil {
			g.allowNets = append(g.allowNets, n)
		}
	}
}

// AllowAll disables blocking entirely (the user opted out, e.g. a
// fully-trusted internal deployment). Kept explicit so it never happens
// implicitly.
func AllowAll() Option { return func(g *Guard) { g.allowAll = true } }

// New builds a Guard with the default deny-non-public policy plus any
// allowlist options.
func New(opts ...Option) *Guard {
	g := &Guard{allowHosts: map[string]bool{}}
	for _, o := range opts {
		o(g)
	}
	return g
}

// parseCIDR accepts "10.0.0.0/8" or a bare "10.1.2.3" (host /32 or /128).
func parseCIDR(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n, nil
	}
	if ip := net.ParseIP(s); ip != nil {
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
	}
	return nil, fmt.Errorf("invalid IP or CIDR %q", s)
}

// allowed reports whether ip is explicitly allowlisted.
func (g *Guard) allowed(ip net.IP) bool {
	for _, n := range g.allowNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isPublic reports whether ip is a routable public address: a global
// unicast that is not in a private (RFC1918 / ULA) or link-local range.
// Loopback, multicast, unspecified, link-local (incl. the cloud metadata
// 169.254.169.254), and private ranges all return false.
func isPublic(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
}

// checkIP returns an error if connecting to ip is not permitted.
func (g *Guard) checkIP(ip net.IP) error {
	if g.allowAll || g.allowed(ip) {
		return nil
	}
	if !isPublic(ip) {
		return fmt.Errorf("egress blocked: %s is not a public address (loopback/private/link-local/metadata); allowlist it explicitly to permit", ip)
	}
	return nil
}

// CheckURL validates a URL before it is fetched: the scheme must be
// http or https, the host must be present, and — best effort — the host
// must not already resolve to a blocked address. It is a fast pre-check
// with clear errors; the dial-time Control hook is the authoritative
// guard against DNS rebinding, so callers fetching through Client/Control
// are protected even if CheckURL is skipped.
func (g *Guard) CheckURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("egress blocked: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("egress blocked: scheme %q not allowed (only http/https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("egress blocked: URL has no host")
	}
	if g.allowAll || g.allowHosts[strings.ToLower(host)] {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return g.checkIP(ip)
	}
	// Hostname: best-effort resolve. Any blocked result fails the
	// pre-check. (DNS can change between here and the dial; Control is
	// the real enforcement.)
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("egress blocked: cannot resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if err := g.checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

// Control is a net.Dialer.Control hook. The dialer calls it with the
// concrete address it is about to connect to, so it enforces the policy
// on the post-resolution IP and defeats DNS rebinding. Wire it into a
// net.Dialer (see Client) or use directly.
func (g *Guard) Control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("egress blocked: cannot parse dial address %q", address)
	}
	return g.checkIP(ip)
}

// Client returns an *http.Client that enforces the guard at dial time and
// re-validates every redirect hop, stripping the Authorization header
// when a redirect crosses to a different host so ambient credentials are
// not leaked to a redirect target. timeout bounds the whole request
// (0 = no timeout). maxRedirects caps redirect hops (<=0 uses a default
// of 10).
func (g *Guard) Client(timeout time.Duration, maxRedirects int) *http.Client {
	if maxRedirects <= 0 {
		maxRedirects = 10
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: g.Control}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			Proxy: http.ProxyFromEnvironment,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("egress blocked: stopped after %d redirects", maxRedirects)
			}
			if err := g.CheckURL(req.URL.String()); err != nil {
				return err
			}
			// Don't forward credentials across a host change.
			if len(via) > 0 && !sameHost(req.URL, via[len(via)-1].URL) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Hostname(), b.Hostname())
}
