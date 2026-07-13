package core

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// maxSidecarErrorLen bounds how much of an error string is written to the
// session error sidecar. Provider failures routinely stringify a whole HTTP
// response body; the sidecar is a durable local file, so we keep enough to
// diagnose the failure without letting one error grow without limit.
const maxSidecarErrorLen = 4096

// sidecarRedactions strip secret-shaped substrings from error text before it
// is persisted. Provider/auth failures frequently embed the request
// (Authorization headers, tokened callback URLs) or the response body, any of
// which can carry a live credential — and unlike the transient in-TUI banner,
// the sidecar keeps it on disk. Each rule replaces the secret while preserving
// surrounding context so the error stays legible. Order matters: the more
// specific header/bearer rules run before the generic key=value ones.
var sidecarRedactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Authorization header value, with or without an explicit scheme.
	{regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(?:bearer\s+)?[A-Za-z0-9._~+/=-]{8,}`), `${1}[REDACTED]`},
	// Bare bearer tokens anywhere in the text.
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), `${1}[REDACTED]`},
	// Well-known API-key / token formats (Anthropic, OpenAI, GitHub, Slack, AWS).
	{regexp.MustCompile(`(?i)\b(sk-ant-[A-Za-z0-9._-]{6,}|sk-[A-Za-z0-9._-]{16,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{8,}|AKIA[0-9A-Z]{16})\b`), `[REDACTED]`},
	// JWTs (header.payload.signature).
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{4,}\b`), `[REDACTED]`},
	// Credential-bearing URL query parameters (e.g. an OAuth callback URL).
	{regexp.MustCompile(`(?i)([?&](?:access_token|refresh_token|id_token|token|api[_-]?key|apikey|client_secret|password|secret|signature|sig|code|key)=)[^&#\s"']+`), `${1}[REDACTED]`},
	// Credential-bearing JSON / key=value fields.
	{regexp.MustCompile(`(?i)("?(?:access_token|refresh_token|id_token|api[_-]?key|apikey|client_secret|password|secret|token)"?\s*[:=]\s*"?)[^"\s,}&]+`), `${1}[REDACTED]`},
}

// RedactSecrets strips secret-shaped substrings (auth headers, bearer/JWT
// tokens, known API-key formats, and credential-bearing URL/JSON fields) from
// text that will be surfaced outside the session file. It is the shared
// redaction the error sidecar and session_inspect both apply; it does not bound
// length (callers cap their own output) and never fails.
func RedactSecrets(text string) string {
	for _, r := range sidecarRedactions {
		text = r.re.ReplaceAllString(text, r.repl)
	}
	return text
}

// redactErrorForSidecar strips secret-shaped substrings from an error string
// and bounds its length before it is written to the durable error sidecar. It
// never fails: worst case it returns the (still length-bounded) input. The
// length bound runs after redaction and backs up to a UTF-8 rune boundary so
// truncation can't emit an invalid trailing rune.
func redactErrorForSidecar(errText string) string {
	errText = RedactSecrets(errText)
	if len(errText) > maxSidecarErrorLen {
		cut := maxSidecarErrorLen
		for cut > 0 && !utf8.RuneStart(errText[cut]) {
			cut--
		}
		errText = errText[:cut] + fmt.Sprintf("… [truncated %d bytes]", len(errText)-cut)
	}
	return errText
}
