package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
)

// extDoctorStaticRow is what a read-only manifest scan can tell about one
// extension directory without spawning anything.
type extDoctorStaticRow struct {
	Scope    string
	Name     string
	Version  string
	Enabled  bool
	Dir      string
	Exec     string
	Manifest string
	Error    string
	Shadowed bool
}

// extDoctor diagnoses extension discovery and registration. It statically
// scans every extension directory (manifest errors, name shadowing) and then
// spins a throwaway manager to capture live load/ready/registration
// diagnostics. It never changes normal fail-soft extension behavior.
//
// The live pass uses the manager's default (untrusted) discovery, so it does
// NOT spawn a project's extensions from an untrusted workspace; those still
// get the static diagnostics. Global extensions get the full live picture.
func extDoctor(version string) error {
	rows := scanExtDoctorStatic()
	if len(rows) == 0 {
		fmt.Fprintln(os.Stdout, "no extensions installed")
		fmt.Fprintln(os.Stdout, "see docs/extensions.md to write your own, or `terva ext install <path|url>`")
		return nil
	}

	cwd, _ := os.Getwd()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mgr := build.NewExtensionManager(config.TervaHome(), cwd, version, "", "", build.NonInteractiveExtHooks{})
	_ = mgr.Discover(ctx)
	mgr.WaitForReady(3 * time.Second)
	diags := mgr.Diagnostics()
	mgr.Stop(500 * time.Millisecond)

	diagByDir := map[string]extdriver.ExtensionDiagnostic{}
	for _, d := range diags {
		diagByDir[d.Dir] = d
	}

	fmt.Fprintln(os.Stdout, "terva extension doctor")
	fmt.Fprintln(os.Stdout)
	for _, row := range rows {
		printExtDoctorRow(os.Stdout, row, diagByDir[row.Dir])
	}
	return nil
}

// scanExtDoctorStatic reads every extension directory's manifest without
// spawning. Project directories win over global on a name clash (the same
// priority Discover uses), so a global extension shadowed by a project one of
// the same name is flagged.
func scanExtDoctorStatic() []extDoctorStaticRow {
	dirs := extensionDirs()
	seen := map[string]bool{}
	var rows []extDoctorStaticRow
	// project first so it wins the name; global entries seen after are shadowed.
	for _, scope := range []string{"project", "global"} {
		dir, ok := dirs[scope]
		if !ok {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // missing directory is fine
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			extDir := filepath.Join(dir, e.Name())
			row := extDoctorStaticRow{
				Scope:    scope,
				Name:     e.Name(),
				Enabled:  true,
				Dir:      extDir,
				Manifest: filepath.Join(extDir, "extension.json"),
			}
			if seen[e.Name()] {
				row.Shadowed = true
			} else {
				seen[e.Name()] = true
			}
			raw, err := os.ReadFile(row.Manifest)
			if err != nil {
				row.Error = "missing extension.json"
				rows = append(rows, row)
				continue
			}
			var m struct {
				Name    string `json:"name"`
				Version string `json:"version"`
				Exec    string `json:"exec"`
				Enabled *bool  `json:"enabled"`
			}
			if err := json.Unmarshal(raw, &m); err != nil {
				row.Error = "parse manifest: " + err.Error()
				rows = append(rows, row)
				continue
			}
			if m.Name != "" {
				row.Name = m.Name
			} else {
				row.Error = "manifest: name is required"
			}
			row.Version = m.Version
			row.Exec = m.Exec
			if m.Enabled != nil {
				row.Enabled = *m.Enabled
			}
			// An extension with no exec is theme-only in terva (it just marks
			// ready, no subprocess), so a missing exec is not an error.
			rows = append(rows, row)
		}
	}
	return rows
}

func printExtDoctorRow(w io.Writer, row extDoctorStaticRow, diag extdriver.ExtensionDiagnostic) {
	status := "ok"
	switch {
	case row.Error != "":
		status = "error"
	case row.Shadowed:
		status = "shadowed"
	case !row.Enabled:
		status = "disabled"
	case row.Exec == "":
		status = "theme-only"
	case diag.FailedReason != "":
		status = "failed to start"
	case diag.Name == "":
		status = "not loaded"
	case diag.ReadyTimedOut:
		status = "ready-timeout"
	case diag.AutoReady:
		status = "auto-ready"
	case !diag.Ready:
		status = "not ready"
	case len(diag.Messages) > 0:
		status = "warning"
	}
	fmt.Fprintf(w, "%s [%s] %s (%s)\n", row.Name, row.Scope, status, row.Dir)
	if row.Version != "" {
		fmt.Fprintf(w, "  version: %s\n", row.Version)
	}
	if row.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", row.Error)
		return
	}
	if row.Shadowed {
		fmt.Fprintln(w, "  note: skipped because a higher-priority extension directory with this name wins")
		return
	}
	if !row.Enabled {
		fmt.Fprintln(w, "  note: disabled in extension.json")
		return
	}
	if row.Exec == "" {
		fmt.Fprintln(w, "  type: theme-only")
		return
	}
	fmt.Fprintf(w, "  exec: %s\n", row.Exec)
	logPath := diag.LogPath
	if logPath == "" {
		logPath = extDoctorLogPath(row.Name)
	}
	if logPath != "" {
		fmt.Fprintf(w, "  log: %s\n", logPath)
	}
	// Why it isn't running, said here rather than left in the log. A launcher
	// that reported progress before failing gets both lines: what it was doing
	// and where it stopped, which is the difference between "my extension is
	// broken" and "my extension needs longer to build than the deadline".
	if diag.Bootstrapping != "" {
		fmt.Fprintf(w, "  bootstrap: %s\n", diag.Bootstrapping)
	}
	if diag.FailedReason != "" {
		fmt.Fprintf(w, "  failed: %s\n", diag.FailedReason)
		fmt.Fprintln(w, "  hint: prebuild the extension, or raise extension_hello_timeout (seconds) in config.json")
		return
	}
	if len(diag.Commands) > 0 {
		fmt.Fprintln(w, "  commands:")
		for _, c := range diag.Commands {
			state := "active"
			if !c.Active {
				state = "shadowed"
			}
			fmt.Fprintf(w, "    /%s (%s)\n", c.Name, state)
		}
	}
	if len(diag.Tools) > 0 {
		fmt.Fprintln(w, "  tools:")
		for _, t := range diag.Tools {
			state := "active"
			if !t.Active {
				state = "shadowed"
			}
			fmt.Fprintf(w, "    %s (%s)\n", t.Name, state)
		}
	}
	for _, msg := range diag.Messages {
		fmt.Fprintf(w, "  warning: %s\n", msg)
	}
}

func extDoctorLogPath(name string) string {
	if name == "" || config.TervaHome() == "" {
		return ""
	}
	return filepath.Join(config.TervaHome(), "logs", "ext-"+name+".log")
}
