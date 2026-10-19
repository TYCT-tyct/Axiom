package mail

import (
	netmail "net/mail"
	"strings"
	"testing"

	"axiom/internal/platform/config"
	"axiom/internal/platform/secret"
)

func TestReadConfig(t *testing.T) {
	t.Parallel()
	sender := netmail.Address{Name: "Axiom", Address: "no-reply@example.com"}
	cases := []struct {
		name    string
		environ []string
		want    Config
	}{
		{name: "nothing set"},
		{
			name:    "host and sender",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_FROM=Axiom <no-reply@example.com>"},
			want:    Config{Host: "smtp.example.com", Port: defaultPort, From: sender},
		},
		{
			name: "credentials and a port",
			environ: []string{
				"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_PORT=465", "AXIOM_SMTP_USERNAME=axiom",
				"AXIOM_SMTP_PASSWORD=planted-password", "AXIOM_SMTP_FROM=no-reply@example.com",
			},
			want: Config{
				Host: "smtp.example.com", Port: 465, Username: "axiom", Password: secret.New("planted-password"),
				From: netmail.Address{Address: "no-reply@example.com"},
			},
		},
		{
			name:    "another submission port",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_PORT=2525", "AXIOM_SMTP_FROM=no-reply@example.com"},
			want:    Config{Host: "smtp.example.com", Port: 2525, From: netmail.Address{Address: "no-reply@example.com"}},
		},
		{
			name:    "an IPv6 host",
			environ: []string{"AXIOM_SMTP_HOST=::1", "AXIOM_SMTP_FROM=no-reply@example.com"},
			want:    Config{Host: "::1", Port: defaultPort, From: netmail.Address{Address: "no-reply@example.com"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := config.New(c.environ)
			got, err := ReadConfig(env)
			if err != nil {
				t.Fatalf("ReadConfig() error = %v", err)
			}
			if envErr := env.Err(); envErr != nil {
				t.Fatalf("the configuration variables are read: %v", envErr)
			}
			if got.Host != c.want.Host || got.Port != c.want.Port || got.Username != c.want.Username ||
				got.From != c.want.From || got.Password.Reveal() != c.want.Password.Reveal() {
				t.Errorf("ReadConfig() = %+v; want %+v", got, c.want)
			}
		})
	}
}

func TestReadConfigProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		environ []string
		want    []string
	}{
		{
			name:    "a sender without a host",
			environ: []string{"AXIOM_SMTP_FROM=no-reply@example.com"},
			want:    []string{"AXIOM_SMTP_HOST: required"},
		},
		{
			name:    "a host without a sender",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com"},
			want:    []string{"AXIOM_SMTP_FROM: required"},
		},
		{
			name:    "a username without a password",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_FROM=no-reply@example.com", "AXIOM_SMTP_USERNAME=axiom"},
			want:    []string{"AXIOM_SMTP_PASSWORD: required"},
		},
		{
			name: "a password without a username",
			environ: []string{
				"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_FROM=no-reply@example.com",
				"AXIOM_SMTP_PASSWORD=planted-password",
			},
			want: []string{"AXIOM_SMTP_USERNAME: required"},
		},
		{
			name:    "a port that is not a number",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_PORT=planted-port", "AXIOM_SMTP_FROM=no-reply@example.com"},
			want:    []string{"AXIOM_SMTP_PORT: want a port from 1 to 65535"},
		},
		{
			name:    "a port beyond the range",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_PORT=70000", "AXIOM_SMTP_FROM=no-reply@example.com"},
			want:    []string{"AXIOM_SMTP_PORT: want a port from 1 to 65535"},
		},
		{
			name:    "a sender that is not an address",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_FROM=planted-sender"},
			want:    []string{"AXIOM_SMTP_FROM: want one address"},
		},
		{
			name:    "a sender that is two addresses",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com", "AXIOM_SMTP_FROM=a@example.com, b@example.com"},
			want:    []string{"AXIOM_SMTP_FROM: want one address"},
		},
		{
			name:    "a host with a scheme",
			environ: []string{"AXIOM_SMTP_HOST=planted://smtp.example.com", "AXIOM_SMTP_FROM=no-reply@example.com"},
			want:    []string{"AXIOM_SMTP_HOST: want a host name"},
		},
		{
			name:    "a host with a port",
			environ: []string{"AXIOM_SMTP_HOST=smtp.example.com:587", "AXIOM_SMTP_FROM=no-reply@example.com"},
			want:    []string{"AXIOM_SMTP_HOST: want a host name"},
		},
		{
			name:    "only a port set",
			environ: []string{"AXIOM_SMTP_PORT=2525"},
			want:    []string{"AXIOM_SMTP_HOST: required", "AXIOM_SMTP_FROM: required"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			checkConfigError(t, c.environ, c.want)
		})
	}
}

// checkConfigError runs one malformed configuration and checks that it fails, naming every expected variable, without
// quoting any value.
func checkConfigError(t *testing.T, environ, wants []string) {
	t.Helper()
	env := config.New(environ)
	got, err := ReadConfig(env)
	if envErr := env.Err(); envErr != nil {
		t.Fatalf("the configuration variables are read: %v", envErr)
	}
	if err == nil {
		t.Fatalf("ReadConfig() = %+v, nil; want an error", got)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ReadConfig() error = %q; want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "planted") {
		t.Errorf("ReadConfig() error = %q; it quotes a value", err)
	}
	if got.Configured() || got.Username != "" || got.Password.Reveal() != "" {
		t.Errorf("ReadConfig() = %+v with an error; want the zero Config", got)
	}
}
