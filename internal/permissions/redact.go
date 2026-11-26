package permissions

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Redacted replaces a secret in the action log.
const Redacted = "[redacted]"

// secretPatterns match strings shaped like secrets: keychain references,
// well-known API key and token formats, bearer tokens and JWTs.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`keychain:[^\s"',;]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),                                      // OpenAI, Anthropic (sk-ant-…)
	regexp.MustCompile(`\b(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{20,}`),                // GitHub
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),                                // AWS access key IDs
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),                              // Slack
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),                                      // Google
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),                                   // GitLab
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), // JWT
	regexp.MustCompile(`(?i)(\bbearer|authorization:\s*basic)\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
}

// tokenRun is a candidate generic token: a long run of key characters.
var tokenRun = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)

// secretFields are argument names whose values are always redacted.
var secretFields = []string{"password", "passwd", "passphrase", "secret", "token", "api_key", "apikey", "access_key", "private_key", "authorization", "credential", "cookie"}

// Redact removes secrets from free text: keychain references, key-shaped
// strings, and long random-looking tokens (mixed upper case, lower case and
// digits, 32 characters or more).
func Redact(s string) string {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			if strings.HasPrefix(re.String(), "(?i)") {
				s = re.ReplaceAllString(s, "$1 "+Redacted)
				continue
			}
			s = re.ReplaceAllString(s, Redacted)
		}
	}
	return tokenRun.ReplaceAllStringFunc(s, func(t string) string {
		if randomLooking(t) {
			return Redacted
		}
		return t
	})
}

// randomLooking reports whether a token mixes upper case, lower case and
// digits, as keys do and words, paths and hex hashes do not.
func randomLooking(t string) bool {
	var upper, lower, digit bool
	for _, r := range t {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

// secretField reports whether an argument name says its value is a secret.
func secretField(name string) bool {
	// Compare without separators, so "X-Api-Key", "apiKey" and "api_key"
	// all match.
	n := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(name))
	for _, f := range secretFields {
		if strings.Contains(n, strings.ReplaceAll(f, "_", "")) {
			return true
		}
	}
	return false
}

// RedactJSON returns tool arguments for the action log with secrets removed:
// every value under a secret-sounding name (password, token, api_key, …) and
// every secret-shaped string. Arguments that are not JSON are redacted as
// text.
func RedactJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return Redact(string(raw))
	}
	b, err := json.Marshal(redactValue(v))
	if err != nil {
		return Redact(string(raw))
	}
	return string(b)
}

func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return Redact(t)
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
		return t
	case map[string]any:
		for k, e := range t {
			if secretField(k) {
				if e != nil {
					t[k] = Redacted
				}
				continue
			}
			t[k] = redactValue(e)
		}
		return t
	}
	return v
}
