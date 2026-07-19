//go:build terva_workflows

package agent

// The terva_workflows registration seam for the `terva workflow`
// subcommand (workstream C of docs/plans/jsengine-code-execution-and-
// workflows.md). This file and its !terva_workflows twin are the only
// places the tag is consulted: the workflow package itself is untagged
// (the default build compiles and tests it); linking it — and sobek —
// into the binary happens exactly here.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/workflow"
	"terva.sh/terva/packages/i18n"
)

func runWorkflowCommand(rawArgs []string, version string) (bool, error) {
	if len(rawArgs) == 0 || rawArgs[0] != "workflow" {
		return false, nil
	}
	if len(rawArgs) > 1 && rawArgs[1] == "run" {
		return true, runWorkflowRun(rawArgs[2:])
	}
	return true, fmt.Errorf("usage: terva workflow run <script.js> [--args <json|@file>] [--resume <run-id>] [--budget-usd N] [--concurrency N] [--timeout DUR] [--cwd DIR]")
}

func runWorkflowRun(argv []string) error {
	fs := flag.NewFlagSet("workflow run", flag.ContinueOnError)
	argsJSON := fs.String("args", "", "JSON value for the script's `args` global, or @path to a JSON file")
	resumeID := fs.String("resume", "", "resume an existing run: completed agent() calls replay from its journal")
	budgetUSD := fs.Float64("budget-usd", 0, "hard spend ceiling for the run (0 = none)")
	concurrency := fs.Int("concurrency", 0, "max simultaneous agents (0 = min(16, cores-2))")
	timeout := fs.Duration("timeout", 0, "bound the whole run (0 = none)")
	cwd := fs.String("cwd", "", "working directory the agents share (default: current)")
	// stdlib flag stops at the first positional; re-parse the remainder so
	// `workflow run script.js --args …` and `workflow run --args … script.js`
	// both work.
	scriptPath := ""
	fsArgs := argv
	for {
		if err := fs.Parse(fsArgs); err != nil {
			return err
		}
		rem := fs.Args()
		if len(rem) == 0 {
			break
		}
		if scriptPath != "" {
			return fmt.Errorf("workflow run: unexpected argument %q", rem[0])
		}
		scriptPath = rem[0]
		fsArgs = rem[1:]
	}
	if scriptPath == "" {
		return fmt.Errorf("workflow run: exactly one script path is required")
	}
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		return err
	}
	var argsVal any
	if raw := strings.TrimSpace(*argsJSON); raw != "" {
		if strings.HasPrefix(raw, "@") {
			b, err := os.ReadFile(raw[1:])
			if err != nil {
				return fmt.Errorf("--args: %w", err)
			}
			raw = string(b)
		}
		if err := json.Unmarshal([]byte(raw), &argsVal); err != nil {
			return fmt.Errorf("--args is not valid JSON: %w", err)
		}
	}
	dir := *cwd
	if dir == "" {
		dir, _ = os.Getwd()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The CLI host drives native `terva --swarm-agent` children only —
	// same minimal construction as `terva raati`. Foreign backends need
	// the workspace host's worker wiring (a later stage); the engine's
	// AllowBackend refuses them loudly rather than running plausible work
	// under the wrong identity.
	eng := swarm.New(swarm.Config{Root: swarm.DefaultRoot(config.TervaHome()), RepoRoot: dir})
	defer eng.StopAllAndWait(5 * time.Second)

	res, err := workflow.Run(ctx, workflow.SwarmEngine{Swarm: eng}, src, workflow.Options{
		Args:        argsVal,
		ResumeID:    *resumeID,
		Root:        filepath.Join(swarm.DefaultRoot(config.TervaHome()), "workflows"),
		Concurrency: *concurrency,
		BudgetUSD:   *budgetUSD,
		Timeout:     *timeout,
		Progress:    func(m string) { fmt.Fprintln(os.Stderr, m) },
	})
	if res.RunID != "" {
		fmt.Fprintln(os.Stderr, i18n.T("workflow %s: run %s — %d agents (%d replayed), $%.4f, %s",
			res.Meta.Name, res.RunID, res.Agents, res.CachedAgents, res.CostUSD, res.Elapsed.Round(time.Millisecond)))
	}
	if err != nil {
		if res.RunID != "" {
			fmt.Fprintln(os.Stderr, i18n.T("resume with: terva workflow run %s --resume %s", scriptPath, res.RunID))
		}
		return err
	}
	out, merr := json.MarshalIndent(res.Value, "", "  ")
	if merr != nil {
		return merr
	}
	fmt.Println(string(out))
	return nil
}
