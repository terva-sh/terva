// The smallest real workflow: fan three agents out in parallel, collect
// what survives, return a value.
//
//   terva workflow run 01-hello-fanout.js
//
// Narration streams to stderr; this script's return value prints to stdout
// as JSON. Every run gets an id — interrupt the run (or hit a transient
// failure) and `--resume <run-id>` replays completed agents from the
// journal instead of re-spending.
export const meta = {
  name: 'hello-fanout',
  description: 'Fan three agents out in parallel and collect their reports',
}

const TOPICS = ['error handling', 'naming', 'test coverage']

phase('Fan out')
// parallel() is a barrier: it resolves once every thunk settles. An agent
// that fails resolves to null rather than sinking the batch, so filter
// with .filter(Boolean) before using the results.
const reports = await parallel(TOPICS.map(topic => () =>
  agent("In one short paragraph, assess this repository's " + topic + '.',
        { label: 'assess:' + topic })))

const kept = reports.filter(Boolean)
log(kept.length + ' of ' + TOPICS.length + ' agents reported')

return { topics: TOPICS, reports: kept }
