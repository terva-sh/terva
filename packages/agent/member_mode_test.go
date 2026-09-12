package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/testsupport"
)

// TestMemberSubcommandRoutesToMemberMode covers the argv shim. `terva member`
// has to reach mode.Member, or the flags below are never read by anything.
func TestMemberSubcommandRoutesToMemberMode(t *testing.T) {
	args, err := build.ParseArgs([]string{"--member", "--hub", "ws://127.0.0.1:8081", "--origin", "neot", "--fleet-token-file", "/tmp/t"})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if args.Mode != mode.Member {
		t.Errorf("mode is %q, want %q", args.Mode, mode.Member)
	}
	if args.MemberHub != "ws://127.0.0.1:8081" {
		t.Errorf("MemberHub is %q", args.MemberHub)
	}
	if args.MemberOrigin != "neot" {
		t.Errorf("MemberOrigin is %q", args.MemberOrigin)
	}
	if args.FleetTokenFile != "/tmp/t" {
		t.Errorf("FleetTokenFile is %q", args.FleetTokenFile)
	}
}

// TestTheFleetTokenNeverLandsInArgv is the criterion that the bearer is read
// from a file and never accepted on the command line.
//
// It does not test today's flag list, which would only restate it. It parses
// the spellings somebody would reach for and then walks every string field of
// build.Args looking for the secret. Adding a --fleet-token later fails this
// test wherever the value happens to land.
//
// argv is not private. Any local user reads it through ps and /proc/cmdline,
// and one fleet token names the whole fleet rather than one daemon.
func TestTheFleetTokenNeverLandsInArgv(t *testing.T) {
	const secret = "s3kr1t-fleet-bearer"
	// Only fleet-token spellings belong here. --token and --web-token are the
	// WEB bearer, a different secret with its own flags, and they legitimately
	// land in build.Args. What must not exist is an argv route to the FLEET
	// bearer, which is checked behaviourally below as well.
	attempts := [][]string{
		{"--member", "--fleet-token", secret},
		{"--member", "--fleet-token=" + secret},
		{"--member", "--fleet-bearer", secret},
		{"--member", "--hub", "ws://h", "--origin", "neot", "--fleet-token", secret},
	}

	for _, argv := range attempts {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			args, err := build.ParseArgs(argv)
			if err != nil {
				// Refusing the flag outright is the strongest possible answer.
				return
			}
			v := reflect.ValueOf(args)
			for i := 0; i < v.NumField(); i++ {
				f := v.Field(i)
				if f.Kind() != reflect.String {
					continue
				}
				if strings.Contains(f.String(), secret) {
					t.Errorf("build.Args.%s holds the fleet token from argv (%q).\n"+
						"The bearer must come from --fleet-token-file only: ps and "+
						"/proc/cmdline are readable by any local user, and this token "+
						"names the whole fleet",
						v.Type().Field(i).Name, f.String())
				}
			}
		})
	}

	// The behavioural half, and the one that would survive somebody adding a
	// fleet token flag. Every argv-supplied bearer terva already has is set
	// here, and the member must STILL refuse to start, because the only source
	// it reads is the file.
	t.Run("an argv bearer does not satisfy the member", func(t *testing.T) {
		err := runMemberMode(context.Background(), build.Args{
			MemberHub:    "ws://h",
			MemberOrigin: "neot",
			Token:        secret,
			WebToken:     secret,
		}, "0.0.0")
		if err == nil {
			t.Fatal("the member started with a bearer that came from argv")
		}
		if !strings.Contains(err.Error(), "--fleet-token-file") {
			t.Errorf("the refusal does not point at the file flag: %v", err)
		}
	})
}

func TestReadFleetToken(t *testing.T) {
	dir := testsupport.TempDir(t)

	good := filepath.Join(dir, "token")
	if err := os.WriteFile(good, []byte("  hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("reads and trims", func(t *testing.T) {
		got, err := readFleetToken(good)
		if err != nil {
			t.Fatalf("readFleetToken: %v", err)
		}
		if got != "hunter2" {
			t.Errorf("token is %q, want hunter2 with the newline trimmed", got)
		}
	})

	t.Run("a missing path is refused by name", func(t *testing.T) {
		_, err := readFleetToken("")
		if err == nil {
			t.Fatal("no --fleet-token-file was accepted")
		}
		if !strings.Contains(err.Error(), "--fleet-token-file") {
			t.Errorf("the refusal does not name the flag to use: %v", err)
		}
	})

	t.Run("an empty file is refused", func(t *testing.T) {
		empty := filepath.Join(dir, "empty")
		if err := os.WriteFile(empty, []byte("\n  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readFleetToken(empty); err == nil {
			t.Error("an empty token file was accepted, so the member would check in with no bearer")
		}
	})

	t.Run("a directory is refused", func(t *testing.T) {
		if _, err := readFleetToken(dir); err == nil {
			t.Error("a directory was accepted as a token file")
		}
	})

	t.Run("a file that does not exist is refused", func(t *testing.T) {
		if _, err := readFleetToken(filepath.Join(dir, "nope")); err == nil {
			t.Error("a missing file was accepted")
		}
	})
}

// TestMemberModeRefusesIncompleteFlags checks the three required flags each
// name themselves, because a daemon that dies with a bare usage dump is a
// daemon somebody debugs at 3am.
func TestMemberModeRefusesIncompleteFlags(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		args build.Args
		want string
	}{
		{"no hub", build.Args{}, "--hub"},
		{"bad hub scheme", build.Args{MemberHub: "http://h"}, "ws://"},
		{"no origin", build.Args{MemberHub: "ws://h"}, "--origin"},
		{"reserved origin", build.Args{MemberHub: "ws://h", MemberOrigin: "local"}, "reserved"},
		{"origin with a separator", build.Args{MemberHub: "ws://h", MemberOrigin: "a/b"}, "--origin"},
		{"no token file", build.Args{MemberHub: "ws://h", MemberOrigin: "neot"}, "--fleet-token-file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runMemberMode(ctx, tc.args, "0.0.0")
			if err == nil {
				t.Fatal("incomplete member flags were accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestTheMemberHalfCarriesNoBuildTag is the criterion that a member runs from a
// binary built without terva_web.
//
// The mode's own file and every file of packages/agent/fleet must stay
// untagged. A //go:build line added to any of them puts the member half behind
// the browser server, which is the thing this mode exists to avoid: a machine
// that only checks in would then need the web build.
//
// This test running at all is half the proof, since it compiles in the untagged
// test binary. The scan below is the other half, because it fails on the change
// rather than on its consequence.
func TestTheMemberHalfCarriesNoBuildTag(t *testing.T) {
	files := []string{"member_mode.go"}
	entries, err := os.ReadDir(filepath.Join("fleet"))
	if err != nil {
		t.Fatalf("reading the fleet package: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			files = append(files, filepath.Join("fleet", e.Name()))
		}
	}
	if len(files) < 5 {
		t.Fatalf("only found %d files to scan, so this test is not looking where it thinks", len(files))
	}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "//go:build") {
				t.Errorf("%s carries a build tag (%s). The member half must compile "+
					"without terva_web, or a machine that only checks in to a hub has "+
					"to build the browser server it never serves", f, line)
			}
			if line == "package fleet" || strings.HasPrefix(line, "package ") {
				break
			}
		}
	}
}
