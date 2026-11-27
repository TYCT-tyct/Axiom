package mail

import (
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

// UseTestNetwork lets the mailer reach a test SMTP server outside this package, such as a container on loopback: the
// policy allows the loopback dial and trusts the server's certificate. Only a test binary can mint the policy, so a
// deployment cannot loosen its sender through this. Call it on a configured mailer before the queue starts.
func (m *Mailer) UseTestNetwork(p egresstest.Policy) {
	m.sender.exit = egress.NewForTesting(p)
	m.sender.roots = p.RootCAs()
}
