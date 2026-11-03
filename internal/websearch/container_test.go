package websearch_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/websearch"
)

// searxngImage is the SearXNG release the adapter's JSON API was checked against (docs.searxng.org, 2026-10-30).
const searxngImage = "searxng/searxng:2026.10.30-ffa88e3ba"

// mockResults is the recorded response the container's json_engine serves. It travels in a sidecar so the test needs
// no external network: SearXNG's own engine parser produces the JSON the adapter reads, instead of a fixture standing
// in for it.
const mockResults = `{"results":[
 {"url":"https://example.com/one","title":"Example One","content":"The first recorded result."},
 {"url":"https://example.org/two","title":"Example Two","content":"The second recorded result."},
 {"url":"https://example.net/three","title":"Example Three","content":"The third recorded result."}
]}`

const searxngSettings = `use_default_settings:
  engines:
    keep_only:
      - mockengine
server:
  secret_key: "axiom-websearch-container-test"
  limiter: false
  image_proxy: false
search:
  formats: [html, json]
engines:
  - name: mockengine
    engine: json_engine
    enable_http: true
    search_url: http://mock/results.json?q={query}
    results_query: results
    url_query: url
    title_query: title
    content_query: content
    disabled: false
`

// TestSearXNGContainer runs one search through a real SearXNG container, whose json_engine serves the recorded
// results. It checks the wire contract the recorded fixture cannot: the path, the format parameter, the JSON shape and
// the result fields a live instance actually writes.
func TestSearXNGContainer(t *testing.T) {
	ctx := t.Context()
	nw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create the test network: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := nw.Remove(stopCtx); err != nil {
			t.Errorf("remove the test network: %v", err)
		}
	})

	mock, err := testcontainers.Run(ctx, "nginx:alpine",
		network.WithNetwork([]string{"mock"}, nw),
		testcontainers.WithExposedPorts("80/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(mockResults), ContainerFilePath: "/usr/share/nginx/html/results.json", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForHTTP("/results.json").WithPort("80/tcp")),
		containertest.WithHostPolicy(),
	)
	if err != nil {
		t.Fatalf("start the mock results server: %v", err)
	}
	containertest.Cleanup(t, mock, "mock results server")

	searxng, err := testcontainers.Run(ctx, searxngImage,
		network.WithNetwork([]string{"searxng"}, nw),
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(searxngSettings), ContainerFilePath: "/etc/searxng/settings.yml", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForHTTP("/healthz").WithPort("8080/tcp")),
		containertest.WithHostPolicy(),
	)
	if err != nil {
		t.Fatalf("start SearXNG: %v", err)
	}
	containertest.Cleanup(t, searxng, "SearXNG")

	client := egress.NewForTesting(egresstest.Loopback(t))
	searcher, err := websearch.Open(websearch.Config{URL: searchURL(t, ctx, searxng)}, client)
	if err != nil {
		t.Fatal(err)
	}
	searchCtx, cancel := context.WithTimeout(ctx, websearch.Timeout)
	defer cancel()
	results, err := searcher.Search(searchCtx, websearch.Query{Text: "axiom", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search() through SearXNG: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("Search() returned %d results, want 3: %+v", len(results), results)
	}
	for i, want := range []string{"https://example.com/one", "https://example.org/two", "https://example.net/three"} {
		if results[i].URL != want {
			t.Errorf("result %d URL = %q, want %q", i, results[i].URL, want)
		}
	}
}

// searchURL is the address the test reaches the SearXNG container on: its exposed port, on the host.
func searchURL(t *testing.T, ctx context.Context, c testcontainers.Container) string {
	t.Helper()
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("find the SearXNG host: %v", err)
	}
	if host == "localhost" {
		host = "127.0.0.1" // Docker Desktop's IPv6 forwarder stalls under load
	}
	port, err := c.MappedPort(ctx, "8080/tcp")
	if err != nil {
		t.Fatalf("find the SearXNG port: %v", err)
	}
	return "http://" + net.JoinHostPort(host, port.Port())
}
