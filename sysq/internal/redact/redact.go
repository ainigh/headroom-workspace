// Package redact removes credentials from text before it leaves sysq
// (design §6.3): API keys, tokens, private keys, passwords in URLs, card
// numbers.
package redact

import "regexp"

// Mask replaces a redacted span.
const Mask = "[REDACTED]"

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\b(bearer|token|basic)\s+[A-Za-z0-9._~+/=-]{16,}`),
	regexp.MustCompile(`(?i)([?&;\s](?:access_token|token|api_key|apikey|key|secret|password|passwd|pwd|auth)=)[^&\s"']+`),
	regexp.MustCompile(`(?i)(--?(?:password|passwd|token|secret|api-key|apikey)[= ])\S+`),
	regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)[^@\s/]+@`),
	regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`),
}

// String returns s with secrets masked and the number of masks applied.
func String(s string) (string, int) {
	n := 0
	for i, re := range patterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			n++
			sub := re.FindStringSubmatch(m)
			switch i {
			case 10, 11, 12:
				return sub[1] + Mask + suffix(i)
			}
			return Mask
		})
	}
	return s, n
}

func suffix(i int) string {
	if i == 12 {
		return "@"
	}
	return ""
}
