//go:build !terva_web

// Package web's no-tag placeholder: the web control-panel server is an opt-in
// build (-tags terva_web). This file keeps the package compilable — and
// feature-detectable via BuiltIn — without the tag, mirroring acp/acp_stub.go.
package web

// BuiltIn reports whether the web control-panel server is compiled into this
// binary. False here; web.go sets it true under -tags terva_web.
const BuiltIn = false
