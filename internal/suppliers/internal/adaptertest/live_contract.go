//go:build contract

package adaptertest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/jsonx"
)

// Recording reports whether the live tests rewrite the adapters' recorded samples from what the systems answered
// (AXIOM_RECORD_TESTDATA=1). Without it they only check.
func Recording() bool { return os.Getenv("AXIOM_RECORD_TESTDATA") == "1" }

// Answer is what a system under test answered.
type Answer struct {
	Status  int
	Header  http.Header
	Body    []byte
	Elapsed time.Duration
}

// Send sends one request to a system under test by plain HTTP, as a person's tool would: the live tests use it to
// configure the system through its own API, to make the relay calls a key is for and to read what the adapter's
// requests would read, which the adapter's client does not hand back to a test. body is JSON-encoded unless nil.
func Send(t testing.TB, method, base, path string, header http.Header, body any) Answer {
	t.Helper()
	var in io.Reader
	if body != nil {
		raw, err := jsonx.MarshalCanonical(body)
		if err != nil {
			t.Fatalf("%s %s: encode the body: %v", method, path, err)
		}
		in = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, in)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	maps.Copy(req.Header, header)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("%s %s: close the answer: %v", method, path, err)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read the answer: %v", method, path, err)
	}
	return Answer{Status: resp.StatusCode, Header: resp.Header, Body: raw, Elapsed: time.Since(start)}
}

// Bearer is the header of a request made with a token or key.
func Bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

// Decode reads the answer's body into out, or fails the test with the body.
func (a Answer) Decode(t testing.TB, out any) {
	t.Helper()
	if err := jsonx.Unmarshal(a.Body, out); err != nil {
		t.Fatalf("answer %d is not what was expected (%v): %.400s", a.Status, err, a.Body)
	}
}

// System is a container image that serves a system under test on one port.
type System struct {
	Name, Image string
	// Port is where the image listens; PortEnv is the variable that moves it, for containers in the host's network.
	Port, PortEnv string
	Env           map[string]string
	// ReadyPath answers 200 once the system serves.
	ReadyPath string
}

// StartSystem runs the image so that it reaches the upstream, waits until it serves, removes it when the test ends, and
// returns the address the test reaches it at. Start the containers it depends on first and pass their settings in Env.
func StartSystem(t testing.TB, r Reach, u *Upstream, s System) string {
	t.Helper()
	ctx := t.Context()
	port, env := s.Port, maps.Clone(s.Env)
	if r.HostNetwork() {
		port = strconv.Itoa(FreePort(t))
		env[s.PortEnv] = port
	}
	c, err := containertest.Start(ctx, func() (*testcontainers.DockerContainer, error) {
		opts := []testcontainers.ContainerCustomizer{testcontainers.WithEnv(env), r.Customize(u)}
		if !r.HostNetwork() {
			opts = append(opts, testcontainers.WithExposedPorts(port+"/tcp"))
		}
		return testcontainers.Run(ctx, s.Image, append(opts, containertest.WithHostPolicy(ctx))...)
	})
	if err != nil {
		t.Fatalf("starting %s: %v", s.Name, err)
	}
	containertest.Cleanup(t, c, s.Name)
	base := "http://127.0.0.1:" + port
	if !r.HostNetwork() {
		if base, err = c.PortEndpoint(ctx, port+"/tcp", "http"); err != nil {
			t.Fatal(err)
		}
	}
	waitServing(t, c, base+s.ReadyPath, s.Name)
	return base
}

// waitServing polls url until it answers 200, or the container's logs say why it did not.
func waitServing(t testing.TB, c testcontainers.Container, url, name string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for !time.Now().After(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			// Only the status is read; a body that fails to close is a connection the next poll does not reuse.
			if closeErr := resp.Body.Close(); closeErr == nil && resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("%s: stopped while waiting for it to serve", name)
		case <-time.After(500 * time.Millisecond):
		}
	}
	logs, err := c.Logs(t.Context())
	if err == nil {
		if tail, err := io.ReadAll(io.LimitReader(logs, 8<<10)); err == nil {
			t.Logf("%s logs: %s", name, tail)
		}
	}
	t.Fatalf("%s did not serve %s within three minutes", name, url)
}

// Clock is the time the adapter under test sees: the machine's, moved forward by what the test advanced it, so that a
// scan pass that covers "up to a moment ago" reaches records made a second ago.
type Clock struct {
	mu     sync.Mutex
	offset time.Duration
}

// Now is the adapter's clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

// Advance moves the adapter's clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

// Scrubber takes what a sample must not keep out of text: the secrets a test knows (each with the placeholder the
// recorded samples use for it), anything shaped like a credential, and the addresses of the machine. It is the one
// place the recorded files are made clean, and Leaks checks the result.
type Scrubber struct {
	known   []knownSecret
	pattern []rule
}

type knownSecret struct{ text, placeholder string }

type rule struct {
	re   *regexp.Regexp
	with string
}

// NewScrubber makes a scrubber that knows the shapes of credentials and addresses.
func NewScrubber() *Scrubber {
	return &Scrubber{pattern: []rule{
		{regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`), "<JWT>"},
		{regexp.MustCompile(`nap_[A-Za-z0-9]{20,}`), "<PAT>"},
		{regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`), "<KEY>"},
		{regexp.MustCompile(`host\.testcontainers\.internal(:\d+)?`), "upstream.example.test"},
		{regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?\b`), "192.0.2.1"},
		{regexp.MustCompile(`localhost(:\d+)?`), "upstream.example.test"},
	}}
}

// Secret makes text read as placeholder wherever it occurs. Longer secrets are replaced first, so that one that
// contains another is not left half done.
func (s *Scrubber) Secret(text, placeholder string) {
	if text == "" {
		return
	}
	s.known = append(s.known, knownSecret{text, placeholder})
	slices.SortStableFunc(s.known, func(a, b knownSecret) int { return len(b.text) - len(a.text) })
}

// Text returns in with the secrets and addresses replaced.
func (s *Scrubber) Text(in string) string {
	for _, k := range s.known {
		in = strings.ReplaceAll(in, k.text, k.placeholder)
	}
	for _, r := range s.pattern {
		in = r.re.ReplaceAllString(in, r.with)
	}
	return in
}

// Leaks lists the secrets and addresses still in text.
func (s *Scrubber) Leaks(text string) (leaks []string) {
	for _, k := range s.known {
		if strings.Contains(text, k.text) {
			leaks = append(leaks, fmt.Sprintf("a secret that reads %q", k.placeholder))
		}
	}
	for _, marker := range []string{"127.0.0.1", "host.testcontainers.internal", "localhost"} {
		if strings.Contains(text, marker) {
			leaks = append(leaks, marker)
		}
	}
	return leaks
}
