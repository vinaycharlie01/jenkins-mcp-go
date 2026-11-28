package loganalysis

import (
	"regexp"
	"strings"
)

// Everything returned to the client passes through Redact: tokens, passwords,
// auth headers, cookies, JWTs, cloud keys and URL credentials are masked.
var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)(["']?(?:proxy-)?authorization["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token|digest)\s+)?[^\s"',;}]+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} [REDACTED]"},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`), "[REDACTED_JWT]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED_AWS_KEY]"},
	{regexp.MustCompile(`(\b[a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@"']+:[^/\s@"']+@`), "${1}[REDACTED]@"},
	{regexp.MustCompile(`(?i)([?&](?:token|access_token|id_token|refresh_token|sig|signature|x-amz-signature|x-amz-credential|x-amz-security-token|api_key|apikey|key|password|secret|code)=)[^&\s"'#]+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)(["']?(?:password|passwd|pwd|secret|client_secret|token|access_token|refresh_token|id_token|api[_-]?key|private[_-]?key|session(?:id)?|cookie|set-cookie|x-api-key|x-auth-token|credentials?)["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&}]+)`), "${1}[REDACTED]"},
}

// Redact masks secrets in s.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}

	return s
}

// Clip redacts and truncates s to n bytes (on a rune boundary).
func Clip(s string, n int) string {
	s = Redact(strings.TrimSpace(s))
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
