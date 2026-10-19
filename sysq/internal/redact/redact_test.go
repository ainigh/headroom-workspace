package redact

import (
	"strings"
	"testing"
)

func TestRedacts(t *testing.T) {
	cases := []string{
		"curl https://x/y?token=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"Authorization: Bearer sk-ant-abc123def456ghi789jkl012",
		"postgres://admin:hunter2@db.local/app",
		"aws AKIAABCDEFGHIJKLMNOP",
		"run --password s3cret!",
		"card 4111 1111 1111 1111",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----",
	}
	secrets := []string{"ghp_abc", "sk-ant-abc", "hunter2", "AKIAABCD", "s3cret", "4111 1111", "abc\n"}
	for i, c := range cases {
		out, n := String(c)
		if n == 0 || strings.Contains(out, secrets[i]) {
			t.Errorf("%q -> %q", c, out)
		}
	}
	keep := "/usr/sbin/postgres -D /opt/homebrew/var/postgresql@17"
	if out, n := String(keep); n != 0 || out != keep {
		t.Errorf("over-redacted: %q", out)
	}
}
