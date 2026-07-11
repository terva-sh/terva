// Package modes implements terva's three run modes: print, json, interactive.
//
// The neutral "run one headless turn, return its final text" primitive that
// print mode is built on now lives in packages/agent/run, so daemon-side code
// (packages/agent/workspace) can reuse it without importing this frontend
// package.
package modes
