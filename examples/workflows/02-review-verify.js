// A two-stage review: stage one reviews a dimension, stage two
// adversarially verifies that review — and reports through a SCHEMA, so
// the script gets a validated object back, not prose to parse.
//
//   terva workflow run 02-review-verify.js
//
// pipeline() has no barrier between stages: the 'bugs' verification can
// run while the 'tests' review is still thinking. Wall-clock is the
// slowest single item chain, not the sum of the slowest stage in each.
export const meta = {
  name: 'review-verify',
  description: 'Review the working tree across dimensions, adversarially verify each finding',
  whenToUse: 'Before opening a PR: an independent review with a refutation pass',
  phases: [
    { title: 'Review', detail: 'one agent per dimension' },
    { title: 'Verify', detail: 'refute or confirm, as structured data' },
  ],
}

const DIMENSIONS = [
  { key: 'bugs', prompt: 'Review the uncommitted changes for correctness bugs. Cite file:line for each finding.' },
  { key: 'tests', prompt: 'Review the uncommitted changes for missing or weakened test coverage. Cite the untested behavior.' },
]

// The schema becomes the verifying agent's deliverable contract: the agent
// reports by calling deliver_result with arguments matching this shape,
// validation failures are retried on ITS side, and this script receives
// the finished object. An agent whose contract goes unmet resolves to null.
const VERDICT = {
  type: 'object',
  properties: {
    real: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['real', 'summary'],
}

const results = await pipeline(DIMENSIONS,
  d => agent(d.prompt, { label: 'review:' + d.key, phase: 'Review' }),
  (review, d) => agent(
    'Adversarially verify the review below. Refute what does not hold up; keep only what does.\n\n' + review,
    { label: 'verify:' + d.key, phase: 'Verify', schema: VERDICT }))

// A pipeline item that failed any stage is null; a surviving item here is
// the verifier's validated verdict object.
const confirmed = results.filter(Boolean).filter(v => v.real)
return { dimensions: DIMENSIONS.length, confirmed }
