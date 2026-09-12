package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/fleet"
	"terva.sh/terva/packages/agent/workspace"
)

// runMemberMode runs the fleet check-in daemon: build this machine's workspace,
// dial the hub, and then SERVE ctrlproto back over the socket we opened.
//
// This file carries no build tag, and that is the point of the mode existing at
// all. packages/agent/fleet carries none either, so a binary built without
// terva_web can still check in to a hub. Folding these flags into `terva web`
// would have forced the browser server onto every member, and a member serves
// its workspace to a hub rather than to a browser.
func runMemberMode(ctx context.Context, args build.Args, version string) error {
	hubURL := strings.TrimSpace(args.MemberHub)
	if hubURL == "" {
		return fmt.Errorf("terva member: --hub is required: give the hub's member endpoint, for example --hub ws://127.0.0.1:8081")
	}
	if !strings.HasPrefix(hubURL, "ws://") && !strings.HasPrefix(hubURL, "wss://") {
		return fmt.Errorf("terva member: --hub %q must start with ws:// or wss://", hubURL)
	}

	origin := strings.TrimSpace(args.MemberOrigin)
	if origin == "" {
		return fmt.Errorf("terva member: --origin is required: it names this machine in the fleet and prefixes every session id the hub shows, for example --origin neot")
	}
	if origin == fleet.LocalOrigin {
		return fmt.Errorf("terva member: --origin %q is reserved for the hub's own workspace, pick another name for this machine", fleet.LocalOrigin)
	}
	if !ctrlproto.ValidOrigin(origin) {
		return fmt.Errorf("terva member: --origin %q is not usable: it becomes the prefix of a federated session id, so it cannot be empty or contain %q", origin, ctrlproto.FederatedIDSep)
	}

	token, err := readFleetToken(args.FleetTokenFile)
	if err != nil {
		return err
	}

	ws, err := workspace.NewWorkspace(args, version)
	if err != nil {
		return err
	}
	defer ws.Close()

	m, err := fleet.NewMember(fleet.MemberOptions{
		HubURL:  hubURL,
		Token:   token,
		Origin:  origin,
		Service: ws,
		Hello:   ctrlproto.ServerHello("terva-member", version),
		OnServed: func(err error) {
			// A served connection ending is ordinary: the hub restarted, the
			// laptop slept, the tunnel dropped. Member.Run redials, so say it
			// once at the level of news rather than of failure.
			if err != nil {
				fmt.Fprintf(os.Stderr, "terva member: hub connection ended (%v), redialling\n", err)
				return
			}
			fmt.Fprintln(os.Stderr, "terva member: hub connection ended, redialling")
		},
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "terva member: checking in to %s as %q\n", hubURL, origin)
	return m.Run(ctx)
}

// readFleetToken reads the shared fleet bearer from a file.
//
// There is deliberately no flag that takes the token as a value. argv is world
// readable through ps and /proc/cmdline, and this secret is worse than the web
// bearer it sits beside: one fleet token names every machine in the fleet
// rather than one daemon. packages/agent/build/args.go already records that
// lesson for --web-token, which exists and is documented as the discouraged
// spelling. Starting the fleet flag set without that mistake costs nothing now
// and cannot be undone later without a deprecation.
func readFleetToken(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("terva member: --fleet-token-file is required: the fleet bearer is read from a file, never from the command line, because argv is readable by any process on this machine")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("terva member: cannot read --fleet-token-file %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("terva member: --fleet-token-file %s is a directory", path)
	}
	// A warning rather than a refusal. systemd LoadCredential= lands at 0400
	// and a hand-made file often does not, and refusing to start over a
	// permission bit would strand somebody at 3am with a daemon that will not
	// boot. Saying it once is enough to get the bit fixed.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "terva member: %s is mode %04o, so other users on this machine can read the fleet token; 0600 or tighter is better\n", path, mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("terva member: cannot read --fleet-token-file %s: %w", path, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("terva member: --fleet-token-file %s is empty: a member with no bearer cannot check in", path)
	}
	return token, nil
}
