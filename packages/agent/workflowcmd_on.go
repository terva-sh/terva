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
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/workflow"
	"terva.sh/terva/packages/agent/workflow/runs"
	"terva.sh/terva/packages/i18n"
)

func runWorkflowCommand(rawArgs []string, version string) (bool, error) {
	if len(rawArgs) == 0 || rawArgs[0] != "workflow" {
		return false, nil
	}
	if len(rawArgs) > 1 {
		switch rawArgs[1] {
		case "run":
			return true, runWorkflowRun(rawArgs[2:])
		case "list":
			return true, runWorkflowList()
		case "show":
			return true, runWorkflowShow(rawArgs[2:])
		}
	}
	return true, fmt.Errorf(`usage:
  terva workflow run <script.js> [--args <json|@file>] [--resume <run-id>] [--budget-usd N] [--concurrency N] [--timeout DUR] [--cwd DIR]
  terva workflow list                     every run: id, status, agents, cost
  terva workflow show <run-id> [--script] the run's record and its journaled results`)
}

// workflowsRoot is where every run's state lives. One expression, three verbs.
func workflowsRoot() string {
	return runs.Root(swarm.DefaultRoot(config.TervaHome()))
}

// runWorkflowList enumerates runs newest-first. The columns are chosen to answer
// one question — is there work here I would otherwise pay for again — so the
// completed/total pair matters more than the id: "1/6" is the whole finding.
func runWorkflowList() error {
	recs, err := runs.ListRecords(workflowsRoot())
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Println(i18n.T("no workflow runs recorded"))
		return nil
	}
	for _, r := range recs {
		done := runs.CompletedCalls(workflowsRoot(), r.RunID)
		total := r.Agents
		agents := fmt.Sprintf("%d", done)
		if total > 0 {
			agents = fmt.Sprintf("%d/%d", done, total)
		}
		// "+3" is the difference between a run to wait for and one to resume.
		if flying, ferr := runs.InFlightCalls(workflowsRoot(), r.RunID); ferr == nil && len(flying) > 0 {
			agents += fmt.Sprintf("+%d", len(flying))
		}
		line := fmt.Sprintf("%-16s  %-10s  %-14s  agents %-7s  $%.4f  %s",
			r.RunID, r.Status(), truncField(r.Name, 14), agents, r.CostUSD, r.Started)
		if r.Resumable(done) {
			line += "  " + i18n.T("← resumable: terva workflow run %s --resume %s", orDash(r.ScriptAt), r.RunID)
		}
		fmt.Println(line)
	}
	return nil
}

// runWorkflowShow prints one run's record and the results it journaled, so a
// finished run's output is readable without the launching process — which is
// the whole gap: the reports were on disk the entire time and unreachable in
// practice.
func runWorkflowShow(argv []string) error {
	fs := flag.NewFlagSet("workflow show", flag.ContinueOnError)
	wantScript := fs.Bool("script", false, "print the run's script source instead of its results")
	// stdlib flag stops at the first positional, so re-parse the remainder —
	// `show <id> --script` and `show --script <id>` must both work. Same shape
	// as runWorkflowRun's loop, and for the same reason: a flag that silently
	// does nothing because of argument order is worse than one that errors.
	runID := ""
	fsArgs := argv
	for {
		if err := fs.Parse(fsArgs); err != nil {
			return err
		}
		rem := fs.Args()
		if len(rem) == 0 {
			break
		}
		if runID != "" {
			return fmt.Errorf("workflow show: unexpected argument %q", rem[0])
		}
		runID = rem[0]
		fsArgs = rem[1:]
	}
	if runID == "" {
		return fmt.Errorf("workflow show: exactly one run id is required")
	}
	root := workflowsRoot()
	rec, err := runs.ReadRecord(root, runID)
	if err != nil {
		return fmt.Errorf("workflow show %s: %w", runID, err)
	}
	if *wantScript {
		if rec.Script == "" {
			return fmt.Errorf("workflow show %s: no script recorded (run predates run records)", runID)
		}
		fmt.Print(rec.Script)
		return nil
	}

	done := runs.CompletedCalls(root, runID)
	fmt.Fprintln(os.Stderr, i18n.T("run %s (%s) — %s, %d result(s) journaled, $%.4f", runID, orDash(rec.Name), string(rec.Status()), done, rec.CostUSD))
	if rec.ScriptAt != "" {
		fmt.Fprintln(os.Stderr, i18n.T("  script: %s (source recorded; --script prints it)", rec.ScriptAt))
	}
	if rec.Err != "" {
		fmt.Fprintln(os.Stderr, i18n.T("  error: %s", rec.Err))
	}
	// What it is working on, or was working on when it stopped — the labels a
	// started row carries, which every reader used to discard.
	if flying, ferr := runs.InFlightCalls(root, runID); ferr == nil && len(flying) > 0 {
		fmt.Fprintln(os.Stderr, i18n.T("  in flight: %d", len(flying)))
		for _, f := range flying {
			name := f.Label
			if name == "" {
				name = f.AgentID
			}
			fmt.Fprintln(os.Stderr, "    "+name)
		}
	}
	if rec.Resumable(done) {
		fmt.Fprintln(os.Stderr, i18n.T("  resume with: terva workflow run %s --resume %s", orDash(rec.ScriptAt), runID))
	}

	results, err := runs.Results(root, runID)
	if err != nil {
		return err
	}
	out, merr := json.MarshalIndent(results, "", "  ")
	if merr != nil {
		return merr
	}
	fmt.Println(string(out))
	return nil
}

func truncField(s string, n int) string {
	if s == "" {
		return "-"
	}
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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

	// Say so before spending. A rerun of an interrupted run pays again for
	// every agent the journal already holds — measured once at $3.3671 for a
	// single replayable agent. Matched on the script SOURCE and cwd, because
	// that is what resume actually keys on; an edited file at the same path
	// would replay nothing.
	if *resumeID == "" {
		if prior, done, ok := runs.FindIncomplete(workflowsRoot(), string(src), dir); ok {
			fmt.Fprintln(os.Stderr, i18n.T("note: run %s of this script stopped with %d completed agent(s) still on disk.", prior.RunID, done))
			fmt.Fprintln(os.Stderr, i18n.T("      replay them instead of paying again: terva workflow run %s --resume %s", scriptPath, prior.RunID))
		}
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
		Root:        workflowsRoot(),
		CWD:         dir,
		ScriptPath:  scriptPath,
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
