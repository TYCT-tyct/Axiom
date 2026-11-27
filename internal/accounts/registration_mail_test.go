package accounts_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/accounts"
	"axiom/internal/platform/containertest"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	axmail "axiom/internal/platform/mail"
)

// mailpitImage is the SMTP sink the registration mail is delivered to: a real SMTP server that requires STARTTLS, as
// a provider's submission port does, and stores what it accepts behind an HTTP API (mailpit.axllent.org, 2026-11-26).
const mailpitImage = "axllent/mailpit:v1.33.1"

// TestOpenRegistrationOverSMTP: an open registration's link travels the whole way a user's does. The registration and
// mail jobs run on the queue, the mail crosses STARTTLS to a real SMTP server, and the token read back from the
// delivered message makes a user who can sign in.
func TestOpenRegistrationOverSMTP(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cert, certPEM, keyPEM := loopbackCertificate(t)
	sink, err := containertest.Start(ctx, func() (*testcontainers.DockerContainer, error) {
		return testcontainers.Run(ctx, mailpitImage,
			testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
			testcontainers.WithFiles(
				testcontainers.ContainerFile{Reader: strings.NewReader(certPEM), ContainerFilePath: "/certs/cert.pem", FileMode: 0o644},
				testcontainers.ContainerFile{Reader: strings.NewReader(keyPEM), ContainerFilePath: "/certs/key.pem", FileMode: 0o644},
			),
			testcontainers.WithEnv(map[string]string{
				"MP_SMTP_TLS_CERT": "/certs/cert.pem", "MP_SMTP_TLS_KEY": "/certs/key.pem", "MP_SMTP_REQUIRE_STARTTLS": "true",
			}),
			testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForHTTP("/readyz").WithPort("8025/tcp")),
			containertest.WithHostPolicy(ctx),
		)
	})
	if err != nil {
		t.Fatalf("start Mailpit: %v", err)
	}
	containertest.Cleanup(t, sink, "Mailpit")

	x := newTestSite(t)
	logger := slog.New(slog.DiscardHandler)
	queue := x.db.NewQueue(logger, platform, nil)
	mailer, err := axmail.Register(ctx, x.db, queue, axmail.Config{
		Host: "127.0.0.1", Port: mappedPort(t, sink, "1025/tcp"), From: mail.Address{Name: "Axiom", Address: "no-reply@example.com"},
	}, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	mailer.UseTestNetwork(egresstest.Loopback(t, cert))
	registration, err := accounts.RegisterRegistration(x.db, queue, mailer, platform, logger)
	if err != nil {
		t.Fatal(err)
	}
	x.s.UseRegistration(registration)
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := queue.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	w := registrationWorld{x, registration, queue}
	w.open(t, false, 100, 1000)

	const email = "smtp@example.com"
	w.registerPending(t, console, email)
	body := deliveredText(t, "http://127.0.0.1:"+strconv.Itoa(mappedPort(t, sink, "8025/tcp")), email)
	const link = "https://" + console + "/console/verify-email#token="
	_, token, ok := strings.Cut(body, link)
	if !ok {
		t.Fatalf("the delivered mail has no verification link: %q", body)
	}
	token, _, _ = strings.Cut(strings.TrimSpace(token), "\n")
	w.verifyAt(t, console, strings.TrimSpace(token))
	if _, err := x.s.SignIn(ctx, accounts.SignInRequest{Host: console, Email: email, Password: password, ClientIP: client}); err != nil {
		t.Fatalf("sign in with the account the mailed link made: %v", err)
	}
}

// deliveredText is the plain-text body of the one message Mailpit holds for to, read once the queue has delivered it.
// The jobs run on the queue's own schedule, so the test asks the sink until the message is there or a minute passes.
func deliveredText(t *testing.T, api, to string) string {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		var found struct {
			Messages []struct{ ID string } `json:"messages"`
		}
		getJSON(t, api+"/api/v1/search?query="+url.QueryEscape("to:"+to), &found)
		if len(found.Messages) == 1 {
			var message struct{ Text string }
			getJSON(t, api+"/api/v1/message/"+url.PathEscape(found.Messages[0].ID), &message)
			return message.Text
		}
		if len(found.Messages) > 1 || time.Now().After(deadline) {
			t.Fatalf("Mailpit holds %d messages for %s, want 1", len(found.Messages), to)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func getJSON(t *testing.T, address string, out any) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close() //nolint:errcheck // the body is read whole below; closing cannot change the result
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", address, response.Status)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("GET %s: %v", address, err)
	}
	if err := jsonx.Unmarshal(raw, out); err != nil {
		t.Fatalf("GET %s: %v", address, err)
	}
}

func mappedPort(t *testing.T, c testcontainers.Container, port string) int {
	t.Helper()
	mapped, err := c.MappedPort(t.Context(), port)
	if err != nil {
		t.Fatalf("find Mailpit's %s: %v", port, err)
	}
	n, err := strconv.Atoi(mapped.Port())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// loopbackCertificate is a self-signed certificate for 127.0.0.1, the name the sender checks, as the certificate it
// trusts and as the PEM files the sink serves.
func loopbackCertificate(t *testing.T) (cert *x509.Certificate, certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{netip.MustParseAddr("127.0.0.1").AsSlice()},
		NotBefore:   time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if cert, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}
