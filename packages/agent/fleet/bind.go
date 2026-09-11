package fleet

import (
	"fmt"
	"net"
	"strings"
)

// UnixPrefix marks a member endpoint address as a unix socket path.
const UnixPrefix = "unix:"

// CheckMemberBindSafety refuses to put the member endpoint anywhere the
// network can reach it while member identity is still a shared bearer token.
//
// Until fleet-identity gives a member a credential of its own, whoever learns
// the token is any member: the token names the fleet, not the machine. The
// compensating control is that the endpoint is not reachable from the network
// at all. A unix socket is confined by filesystem permissions, and a loopback
// address is confined to this host.
//
// Anything else is refused here rather than warned about. The ticket that
// asked for this said it plainly: a comment telling an operator not to expose
// it is not a control. A reachable member endpoint under a shared secret is a
// remote code execution surface, because driving a daemon is the point of it.
//
// Reaching a member on another machine still works, but not for the reason an
// earlier version of this comment gave. Check-in does not remove the
// reachability requirement, it concentrates it. A member never needs an address,
// because it dials. The hub always needs one, because it accepts. Concentrating
// that requirement in a single host is the trade this direction makes, and it is
// a good trade, but it does not exempt the hub from it.
//
// So while this refusal stands, a remote member reaches the hub through a
// tunnel: it forwards a local port to the hub's loopback and dials that.
// TKT-01M26M4HKR asks whether a tailnet address should become a third confined
// case, which would retire the tunnel. Note there that 100.64.0.0/10 is RFC 6598
// shared address space rather than proof of a tailnet, so it is not a prefix
// test.
func CheckMemberBindSafety(addr string) error {
	if strings.HasPrefix(addr, UnixPrefix) {
		if strings.TrimPrefix(addr, UnixPrefix) == "" {
			return fmt.Errorf("terva fleet: %q names no unix socket path", addr)
		}
		return nil
	}
	if isLoopbackAddr(addr) {
		return nil
	}
	return fmt.Errorf("terva fleet: refusing to bind the member endpoint to %q. "+
		"Member identity is still a shared bearer token, so anyone who reaches this "+
		"port and learns the token can drive every session on this host. Bind a "+
		"loopback address or a %spath until fleet-identity lands. A member on another "+
		"machine reaches that loopback through a tunnel: forward a local port to it "+
		"over SSH or a tailnet, and point the member at the forwarded port", addr, UnixPrefix)
}

// isLoopbackAddr reports whether addr binds only this host. It matches the
// web server's rule deliberately, including the wildcards: an empty host,
// 0.0.0.0 and :: all bind every interface and are not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	case "", "0.0.0.0", "::":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Listen binds the member endpoint, applying CheckMemberBindSafety first so
// the refusal happens before a socket exists rather than after.
func Listen(addr string) (net.Listener, error) {
	if err := CheckMemberBindSafety(addr); err != nil {
		return nil, err
	}
	if strings.HasPrefix(addr, UnixPrefix) {
		return net.Listen("unix", strings.TrimPrefix(addr, UnixPrefix))
	}
	return net.Listen("tcp", addr)
}
