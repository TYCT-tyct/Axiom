package gateway

import (
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/kv"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/websearch"
)

// Call is a request that has been admitted: its reservation is committed and its route is cut to the entries that
// reservation covers. Run gets it; nothing in it changes after that but what Run does through its methods.
type Call struct {
	ID      uuid.UUID // the request ID, which is also the call number
	At      time.Time // one start for the call log and the reservation: the request's arrival, before admission
	Format  protocol.Format
	Compact bool // the Responses compact endpoint: the upstream answers whole and its body is passed through
	Request protocol.Request
	Body    []byte
	search  string // the web plugin's query, read before admission and never logged
	// searchBackend is the backend the plugin's one search runs through, nil when the plugin uses the model's own
	// search instead. nativeSearch is the most searches the gateway turns on upstream, 0 when it turns on none.
	searchBackend *websearch.Backend
	nativeSearch  int
	serverLoop    bool                      // openrouter:web_search runs the model-side loop through searchBackend
	searchPrice   platform.UnitPrice        // the backend's published price per search, zero to use the model's
	files         []protocol.FileAnnotation // the annotations a parsed Chat reply returns, so the client can skip reparsing
	Header        http.Header               // the client's values of the headers that may go upstream, which headers.go derives per attempt
	cache         cachePlan                 // the response cache this call may read or write, zero when it did not ask
	presetCache   *bool                     // the preset's cache_enabled, nil when it does not decide the switch
	presetTTL     int64                     // the preset's cache_ttl_seconds, 0 when absent
	Session       routing.SessionKey
	Admission     accounts.Admission
	Admitted      billing.Admitted
	Hold          *billing.Hold
	Plan          routing.Plan

	site        accounts.Site
	caller      accounts.Caller
	host        string
	ip          netip.Addr
	snap        *catalog.Snapshot
	models      []catalog.ModelID
	auto        bool // the request named openrouter/auto: routing ranks every model the key may call
	free        bool // the request named openrouter/free: routing ranks only the free models the key may call
	sticky      routing.EndpointID
	stickyCache *kv.Client
	stickyKey   string
	over        routing.Override // what the request's provider object narrows the route by
	accountRate int
	rateReset   int64
	rate        int  // requests of the key in the rate window, counted before admission
	gated       bool // the file parser's gate counted the call already; fillCall does not count it again

	record *observability.Slot // this call's room in the call log, taken before admission

	mu       sync.Mutex
	releases []func()
}

// hold registers something to give back when the call is done, in reverse order.
func (c *Call) hold(release func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases = append(c.releases, release)
}

// Done gives back the call's in-flight slots and the memory of its body. It is safe to call more than once.
func (c *Call) Done() {
	c.mu.Lock()
	releases := c.releases
	c.releases = nil
	c.mu.Unlock()
	for _, release := range slices.Backward(releases) {
		release()
	}
}
