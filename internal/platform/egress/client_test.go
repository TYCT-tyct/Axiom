package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/telemetry"
)

func request(t *testing.T, target string) *http.Request {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func mustClose(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Error(err)
	}
}

func TestClientBoundary(t *testing.T) {
	client, err := New(Direct)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, target := range []string{"http://127.0.0.1/", "http://[::1]/", "http://0x7f.0.0.1/", "http://[::ffff:127.0.0.1]/"} {
		if err := requestFailure(t, client, request(t, target)); !errors.Is(err, ErrAddressDenied) {
			t.Errorf("%s: %v", target, err)
		}
	}
	req := request(t, "http://127.0.0.1/").WithContext(t.Context())
	if err := requestFailure(t, client, req); !errors.Is(err, ErrDeadlineRequired) {
		t.Errorf("request without deadline: %v", err)
	}
	if _, err := New(Exit("unsupported")); err == nil {
		t.Error("accepted unsupported exit")
	}
}

func TestLoopbackRedirectAndUserAgent(t *testing.T) {
	var followed atomic.Int32
	server := redirectServer(t, &followed)
	defer server.Close()
	client := NewForTesting(egresstest.Loopback(t))
	defer client.CloseIdleConnections()
	for _, path := range []string{"/", "/malformed"} {
		req := request(t, server.URL+path)
		req.URL.RawQuery = "host=" + req.URL.Host
		req.Host = "forged.invalid"
		req.Header["user-agent"] = []string{"forged"}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer mustClose(t, resp.Body)
		if resp.StatusCode != http.StatusFound || followed.Load() != 0 {
			t.Fatalf("redirect followed: status=%d count=%d", resp.StatusCode, followed.Load())
		}
		if req.Host != "forged.invalid" || !reflect.DeepEqual(req.Header, http.Header{"user-agent": []string{"forged"}}) {
			t.Error("client modified caller's request")
		}
	}
}

// The server fails the test if reached, so each case also fails when its own clause of the check is removed.
func TestRequestValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("invalid request reached the server")
	}))
	defer server.Close()
	host := server.Listener.Addr().String()
	client := NewForTesting(egresstest.Loopback(t))
	defer client.CloseIdleConnections()
	opaque := request(t, "http://"+host+"/")
	opaque.URL.Opaque = "/credential-path"
	serverSide := request(t, "http://"+host+"/")
	serverSide.RequestURI = "/"
	noURL := request(t, "http://"+host+"/")
	noURL.URL = nil
	body := &closeRecorder{Reader: strings.NewReader("credential-body")}
	userinfo := request(t, "http://credential-user:credential-password@"+host+"/")
	userinfo.Body = body
	for name, req := range map[string]*http.Request{
		"userinfo":       userinfo,
		"opaque":         opaque,
		"empty host":     request(t, "http:///credential-path"),
		"ftp":            request(t, "ftp://"+host+"/"),
		"file":           request(t, "file:///credential-path"),
		"server request": serverSide,
		"no URL":         noURL,
		"nil":            nil,
	} {
		t.Run(name, func(t *testing.T) {
			err := requestFailure(t, client, req)
			if !errors.Is(err, ErrInvalidURL) || strings.Contains(err.Error(), "credential-") {
				t.Errorf("not rejected as an invalid URL without leaking it: %v", err)
			}
		})
	}
	if !body.closed {
		t.Error("rejected request body was not closed")
	}
}

type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

func TestZeroTestPolicyCannotAllowLoopback(t *testing.T) {
	client := NewForTesting(egresstest.Policy{})
	defer client.CloseIdleConnections()
	if err := requestFailure(t, client, request(t, "http://127.0.0.1/")); !errors.Is(err, ErrAddressDenied) {
		t.Fatal(err)
	}
}

func TestReadBody(t *testing.T) {
	for _, tc := range []struct {
		body string
		max  int64
		want string
		err  bool
	}{
		{"", 0, "", false},
		{"abc", 3, "abc", false},
		{"abc", 4, "abc", false},
		{"a", 0, "", true},
		{"abcd", 3, "", true},
		{"", -1, "", true},
		{"", math.MaxInt64, "", true},
	} {
		got, err := ReadBody(strings.NewReader(tc.body), tc.max)
		if (err != nil) != tc.err || string(got) != tc.want {
			t.Errorf("ReadBody(%q, %d) = %q, %v", tc.body, tc.max, got, err)
		}
	}
	if _, err := ReadBody(io.MultiReader(strings.NewReader("abc"), failingReader{}), 4); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read failure lost: %v", err)
	}
	body := strings.NewReader("abcdef")
	if _, err := ReadBody(body, 3); !errors.Is(err, ErrBodyTooLarge) || body.Len() != 2 {
		t.Errorf("read beyond the extra probe byte: remaining=%d error=%v", body.Len(), err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestRequestDeadlineCoversBody(t *testing.T) {
	for _, major := range []int{1, 2} {
		t.Run(fmt.Sprintf("HTTP/%d", major), func(t *testing.T) {
			client, url := serve(t, major, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Error(err)
				}
				<-r.Context().Done()
			})
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			resp, err := client.Do(request(t, url).WithContext(ctx))
			if err != nil {
				t.Fatal(err)
			}
			defer mustClose(t, resp.Body)
			if _, err := ReadBody(resp.Body, 1); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("body did not respect deadline: %v", err)
			}
		})
	}
}

// Without the refusal Do returns the 101 and ReadBody on its body blocks past the request deadline.
func TestRefusesProtocolSwitch(t *testing.T) {
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		closed <- errors.Join(switchAndWait(conn), conn.Close())
	}()
	client := NewForTesting(egresstest.Loopback(t))
	defer client.CloseIdleConnections()
	err = requestFailure(t, client, request(t, "http://"+listener.Addr().String()))
	mustClose(t, listener)
	if !errors.Is(err, errProtocolSwitch) {
		t.Errorf("101 response not refused: %v", err)
	}
	if err := <-closed; !errors.Is(err, io.EOF) {
		t.Errorf("connection left open after the refused 101: %v", err)
	}
}

// switchAndWait answers one request with 101 and then stays silent; only the client closing the connection ends the
// read early, with io.EOF.
func switchAndWait(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	if _, err := http.ReadRequest(reader); err != nil {
		return err
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	_, err := reader.ReadByte()
	return err
}

func redirectServer(t *testing.T, followed *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			followed.Add(1)
		}
		if r.UserAgent() != "Axiom" || r.Host != r.URL.Query().Get("host") {
			t.Errorf("unexpected identity: UA=%q Host=%q", r.UserAgent(), r.Host)
		}
		w.Header().Set("Location", "/next")
		if r.URL.Path == "/malformed" {
			w.Header().Set("Location", "http://credential-user:credential-password@[invalid")
		}
		w.WriteHeader(http.StatusFound)
	}))
}

// BenchmarkEgressSpan is what the egress.request span costs for one outbound request: "unsampled" is the provider
// serve builds while no exporter is attached (telemetry.New samples nothing), "sampled" the same with a span
// processor, as when an exporter is. An unsampled span records nothing, so it builds no attributes; bench-compare
// fails if either grows.
func BenchmarkEgressSpan(b *testing.B) {
	for name, processors := range map[string][]sdktrace.SpanProcessor{
		"unsampled": nil,
		"sampled":   {sdktrace.NewSimpleSpanProcessor(tracetest.NewNoopExporter())},
	} {
		node, err := telemetry.New(processors...)
		if err != nil {
			b.Fatal(err)
		}
		client := &Client{tracer: node.Traces.Tracer("axiom/egress")}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, span := client.startSpan(b.Context(), "upstream.example")
				recordStatus(span, http.StatusOK)
				span.End()
			}
		})
	}
}

func requestFailure(t *testing.T, client *Client, req *http.Request) error {
	t.Helper()
	resp, err := client.Do(req)
	if err == nil {
		defer mustClose(t, resp.Body)
		t.Fatal("request unexpectedly succeeded")
	}
	return err
}
