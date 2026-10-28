package accounts

import (
	"strings"
	"testing"
)

func TestPasswordMailLanguagesAndEscaping(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ language, link, want string }{
		{"zh", "https://main.example/reset-password#token=example", "重置密码"},
		{"en", "https://main.example/reset-password#token=example", "reset your password"},
		{"zh", "", "密码已变更"},
		{"en", "", "password changed"},
	} {
		m := passwordMail("user@example.com", "<b>Site</b>\nInjected", c.language, c.link)
		if !strings.Contains(m.Subject, c.want) || strings.Contains(m.Subject, "\n") {
			t.Fatalf("subject: %q", m.Subject)
		}
		if strings.Contains(m.HTML, "<b>Site</b>") || !strings.Contains(m.HTML, "&lt;b&gt;Site&lt;/b&gt;") {
			t.Fatal("brand HTML injection")
		}
		if c.link != "" && (!strings.Contains(m.HTML, c.link) || !strings.Contains(m.Text, "30")) {
			t.Fatal("missing link or expiry")
		}
	}
}
