// Package assets holds static resources embedded in the terva binary.
// Currently just the terva logo served on the OAuth callback pages.
package assets

import _ "embed"

// LogoPNG is the terva mark (hexagonal harness + wildcard; see
// assets/brand/) as 512px PNG bytes.
// Used by the interactive welcome banner; decoded once and rasterized
// to Unicode half-blocks so it renders on any terminal without needing
// inline image support.
//
//go:embed terva-logo.png
var LogoPNG []byte
