package permissions_test

import (
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/permissions"
)

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"use keychain:claude for it", "use [redacted] for it"},
		{"key sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv", "key [redacted]"},
		{"token ghp_abcdefghijklmnopqrstuvwxyz0123456789", "token [redacted]"},
		{"AKIAABCDEFGHIJKLMNOP", "[redacted]"},
		{"Authorization: Bearer abc.def-ghi_jkl", "Authorization: Bearer [redacted]"},
		{"Authorization: Basic dXNlcjpwYXNz", "Authorization: Basic [redacted]"},
		{"a basic understanding", "a basic understanding"},
		{"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "jwt [redacted]"},
		{"random Zx9Qp2Lm8Rt4Vb6Nc1Hj7Kd3Fs5Gw0Ae", "random [redacted]"},
		// Not secrets: words, URLs, paths and hex hashes stay.
		{"open https://www.youtube.com/watch?v=dQw4w9WgXcQ", "open https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		{"commit 3f786850e387550fdab836ed7e6dc881de23001b", "commit 3f786850e387550fdab836ed7e6dc881de23001b"},
		{"/home/me/projects/footman/internal/permissions/redact.go", "/home/me/projects/footman/internal/permissions/redact.go"},
		{"set the volume to 40 percent", "set the volume to 40 percent"},
	}
	for _, c := range cases {
		if got := permissions.Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactJSON(t *testing.T) {
	got := permissions.RedactJSON([]byte(`{"to":"bob@example.com","password":"hunter2","headers":{"Authorization":"x","X-Api-Key":"k"},` +
		`"body":"my key is sk-proj-AbCdEfGhIjKlMnOpQrSt","list":["keychain:mail",3],"retries":2}`))
	for _, secret := range []string{"hunter2", `"x"`, `"k"`, "sk-proj", "keychain:mail"} {
		if strings.Contains(got, secret) {
			t.Errorf("RedactJSON left %s in %s", secret, got)
		}
	}
	for _, kept := range []string{"bob@example.com", `"retries":2`, "my key is [redacted]", `"password":"[redacted]"`} {
		if !strings.Contains(got, kept) {
			t.Errorf("RedactJSON lost %s: %s", kept, got)
		}
	}
	if got := permissions.RedactJSON([]byte(`not json keychain:x`)); got != "not json [redacted]" {
		t.Errorf("non-JSON args = %q", got)
	}
	if got := permissions.RedactJSON(nil); got != "" {
		t.Errorf("no args = %q", got)
	}
}
