// A parameterized, spend-bounded sweep.
//
//   terva workflow run 03-args-budget.js \
//     --args '{"paths":["packages/agent","packages/core"]}' --budget-usd 2
//
// `args` is the --args value, verbatim. `budget` is in US DOLLARS (terva's
// documented divergence from Claude Code's token budgets): budget.total is
// the ceiling or null, budget.remaining() is Infinity without one.
export const meta = {
  name: 'audit-paths',
  description: 'Audit the named paths, one agent each, under a spend ceiling',
  whenToUse: 'Sweeping a list of directories with a hard cost bound',
}

if (!args || !Array.isArray(args.paths) || args.paths.length === 0) {
  throw new Error('pass --args \'{"paths":["dir", …]}\' — the directories to audit')
}

// Workflow scripts are deterministic on purpose: wall-clock time and
// randomness are unavailable, because --resume re-runs the script and
// matches every agent() call against the journal — that only works if
// replay re-derives identical calls. Anything run-specific (a timestamp,
// a label) comes in through args:
const runLabel = args.label || 'audit'

// Sequential on purpose: each iteration reads the spend the previous
// agents actually accrued, so the ceiling check is honest. A parallel
// fan-out would check remaining() before any cost had landed.
const reports = []
for (let i = 0; i < args.paths.length; i++) {
  const p = args.paths[i]
  if (budget.total && budget.remaining() < 0.25) {
    log('stopping before ' + p + ': $' + budget.remaining().toFixed(2) + ' left under the ceiling')
    break
  }
  const r = await agent('Audit ' + p + ' for dead code and unused exports. Be concise.',
                        { label: runLabel + ':' + i })
  if (r) reports.push({ path: p, report: r })
}

return { audited: reports.length, requested: args.paths.length, reports }
