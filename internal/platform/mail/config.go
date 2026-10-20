package mail

import (
	"errors"
	"fmt"
	netmail "net/mail"
	"net/netip"
	"strconv"
	"strings"

	"axiom/internal/platform/config"
	"axiom/internal/platform/secret"
)

// The variables that configure SMTP. They are named here, where they are read, so the errors and the documentation
// spell them the same way.
const (
	hostVar     = "AXIOM_SMTP_HOST"
	portVar     = "AXIOM_SMTP_PORT"
	usernameVar = "AXIOM_SMTP_USERNAME"
	passwordVar = "AXIOM_SMTP_PASSWORD" //nolint:gosec // a variable name, not a credential
	fromVar     = "AXIOM_SMTP_FROM"

	// defaultPort is the submission port new-api defaults to (common/constants.go SMTPPort = 587, 2026-10-19); port
	// 465 speaks implicit TLS instead, which modeFor chooses.
	defaultPort = 587
)

// Config is the SMTP server one node sends through, the sender its messages come from and the credentials it
// authenticates with. The zero value means email is disabled.
type Config struct {
	Host     string
	Port     int
	Username string          // empty sends without authentication, for a relay that admits this node by address
	Password secret.Secret   // never printed: the type redacts it in every formatter
	From     netmail.Address // the From header and the envelope sender
}

// Configured reports whether email is enabled. A node with none of the variables set has the zero Config; a partly
// set one fails ReadConfig instead of reaching this state.
func (c Config) Configured() bool { return c.Host != "" }

// ReadConfig reads the SMTP settings from env. With none of them set, email is disabled and the zero Config is
// returned, so a node without an SMTP account runs every other feature. Setting any one of them arms the whole
// configuration: a partly set one is a startup error, because the node would otherwise discover at the first
// password reset that it cannot send mail.
//
// Errors name the variable and the shape wanted, never the value: the password is a secret, and a secret assigned to
// the wrong variable must not reach the logs.
func ReadConfig(env *config.Env) (Config, error) {
	raw := rawSMTP{
		host:     env.String(hostVar, ""),
		port:     env.String(portVar, ""),
		username: env.String(usernameVar, ""),
		password: env.String(passwordVar, ""),
		from:     env.String(fromVar, ""),
	}
	if !raw.configured() {
		return Config{}, nil
	}
	return raw.parse()
}

// rawSMTP is the five values as the environment spells them, before any check.
type rawSMTP struct {
	host, port, username, password, from string
}

// configured reports whether the operator meant to configure email at all.
func (r rawSMTP) configured() bool { return r != rawSMTP{} }

// parse checks the whole configuration at once and reports every problem it finds, so a node started with half of it
// hears about all of it at startup rather than at the first password reset.
func (r rawSMTP) parse() (Config, error) {
	port, portErr := r.portNumber()
	host, hostErr := r.hostName()
	from, fromErr := r.sender()
	if err := errors.Join(portErr, hostErr, fromErr, r.credentials()); err != nil {
		return Config{}, err
	}
	return Config{Host: host, Port: port, Username: r.username, Password: secret.New(r.password), From: from}, nil
}

func (r rawSMTP) portNumber() (int, error) {
	if r.port == "" {
		return defaultPort, nil
	}
	port, err := strconv.Atoi(r.port)
	if err != nil || port < 1 || port > 65535 {
		return defaultPort, fmt.Errorf("%s: want a port from 1 to 65535", portVar)
	}
	return port, nil
}

func (r rawSMTP) hostName() (string, error) {
	if r.host == "" {
		return "", fmt.Errorf("%s: required to send email", hostVar)
	}
	if err := checkHost(r.host); err != nil {
		return "", err
	}
	return r.host, nil
}

func (r rawSMTP) sender() (netmail.Address, error) {
	switch {
	case r.from == "":
		return netmail.Address{}, fmt.Errorf("%s: required to send email", fromVar)
	case strings.ContainsAny(r.from, "\r\n"):
		return netmail.Address{}, errFrom
	}
	from, err := netmail.ParseAddress(r.from)
	if err != nil {
		return netmail.Address{}, errFrom
	}
	// The envelope path and the From header are both written from this address, so what the envelope cannot carry is a
	// configuration error: a node whose own address cannot be sent would fail at the first password reset.
	if _, err := mailbox(*from); err != nil {
		return netmail.Address{}, errFrom
	}
	return *from, nil
}

// credentials checks that the account is either complete or absent: a username without a password would fail at the
// first send, after the caller's transaction has committed.
func (r rawSMTP) credentials() error {
	switch {
	case r.username != "" && r.password == "":
		return fmt.Errorf("%s: required when %s is set; leave both unset to send without authentication",
			passwordVar, usernameVar)
	case r.username == "" && r.password != "":
		return fmt.Errorf("%s: required when %s is set; leave both unset to send without authentication",
			usernameVar, passwordVar)
	}
	return nil
}

// errFrom is one message for both malformed spellings of the sender: a parser detail would say more about the value
// than an operator needs.
var errFrom = fmt.Errorf("%s: want one address such as Axiom <no-reply@example.com>", fromVar)

// checkHost refuses what net.JoinHostPort would turn into a different address, such as a URL or a port.
func checkHost(host string) error {
	if strings.ContainsAny(host, " \t\r\n/") || strings.Contains(host, ":") && !isIP(host) {
		return fmt.Errorf("%s: want a host name such as smtp.example.com, without a scheme or port", hostVar)
	}
	return nil
}

func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}
