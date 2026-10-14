//go:build !terva_no_telegram

package agent

// leanArtifact reports whether this binary is the terva-min build, so that
// `terva update` fetches the same flavor it replaces. terva_no_telegram is the
// tag every min build sets (.goreleaser.yaml, `just build-min`).
const leanArtifact = false
