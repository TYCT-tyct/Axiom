//go:build load

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// loadDriver is the clients of a load run: calls sent from a number of loopback addresses, since the node limits what
// one address may have in flight (gateway.Config.IPInFlight), and read to their last byte, timed and joined with what
// the upstream saw of them.
type loadDriver struct {
	clients  []*http.Client // one for each source address
	next     atomic.Uint64
	upstream *loadUpstream
	public   atomic.Pointer[string] // the node's public address, which changes with every node
	pads     sync.Map               // size -> []byte of that many letters, shared by the calls that carry them
}

// sourceAddresses is how many loopback addresses calls come from: the node lets one address have 500 calls in flight
// and a load of 5000 must stay under that on each.
const sourceAddresses = 16

func newLoadDriver(upstream *loadUpstream) *loadDriver {
	d := &loadDriver{upstream: upstream}
	for i := range sourceAddresses + 1 { // the last one is the cold-key flood's
		dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 1, byte(i+1))}, KeepAlive: 30 * time.Second}
		d.clients = append(d.clients, &http.Client{Transport: &http.Transport{
			DialContext: dialer.DialContext, MaxIdleConnsPerHost: 64, IdleConnTimeout: 30 * time.Second, DisableCompression: true,
		}})
	}
	return d
}

func (d *loadDriver) close() {
	for _, c := range d.clients {
		c.CloseIdleConnections()
	}
}

// callSpec says who makes a call and what it carries.
type callSpec struct {
	client int    // the source address, below sourceAddresses; sourceAddresses itself is the flood's
	key    string // the call key
	pad    int    // bytes of filler in the message, which make the request as large as the scenario wants
	group  string
}

const callTail = `"}],"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{}}}]}`

func (d *loadDriver) padding(n int) []byte {
	if v, ok := d.pads.Load(n); ok {
		return v.([]byte)
	}
	v, _ := d.pads.LoadOrStore(n, bytes.Repeat([]byte("a"), n))
	return v.([]byte)
}

func (d *loadDriver) request(ctx context.Context, id uint64, c callSpec) (*http.Request, int, error) {
	head := fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":8192,"stream":true,"messages":[{"role":"user","content":"%s%d; `, idMarker, id)
	pad := d.padding(c.pad)
	size := len(head) + len(pad) + len(callTail)
	body := io.MultiReader(strings.NewReader(head), bytes.NewReader(pad), strings.NewReader(callTail))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+*d.public.Load()+"/v1/messages", body)
	if err != nil {
		return nil, 0, err
	}
	req.ContentLength = int64(size)
	req.Host = "api.main.example"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", "claude-cli/2.0.5 (external, cli)")
	return req, size, nil
}

// run makes one call and reads its answer to the end. A call that ctx ended is the scenario's doing and is marked so.
func (d *loadDriver) run(ctx context.Context, c callSpec) outcome {
	id := d.next.Add(1)
	o := outcome{id: id, group: c.group, start: time.Now()}
	req, size, err := d.request(ctx, id, c)
	if err != nil {
		o.err = err.Error()
		return o
	}
	o.bodyBytes = size
	resp, err := d.clients[c.client].Do(req)
	if err != nil {
		o.err = shorten(err.Error())
		o.cancelled = ctx.Err() != nil
		o.up, o.seen = d.upstream.take(id)
		return o
	}
	defer resp.Body.Close() //nolint:errcheck // everything wanted from the body is read
	o.status = resp.StatusCode
	tail, err := readStream(resp.Body, &o)
	o.eof = time.Now()
	complete := errors.Is(err, io.EOF)
	switch {
	case err != nil && !complete:
		o.err = shorten(err.Error())
		o.cancelled = ctx.Err() != nil
	case resp.StatusCode != http.StatusOK:
		o.err = shorten(string(tail))
	default:
		o.done = complete && bytes.Contains(tail, []byte("message_stop"))
		o.pending = bytes.Contains(tail, []byte("billing_pending"))
	}
	o.up, o.seen = d.upstream.take(id)
	return o
}

// readStream reads the body to its end, stamping the first byte, and returns the last bytes of it, which hold the ending.
func readStream(body io.Reader, o *outcome) (tail []byte, err error) {
	buf := make([]byte, 4096)
	for err == nil {
		var n int
		n, err = body.Read(buf)
		if n > 0 {
			if o.first == 0 {
				o.first = time.Since(o.start)
			}
			tail = append(tail, buf[:n]...)
			tail = tail[max(0, len(tail)-512):]
		}
	}
	return tail, err
}

func shorten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 100 {
		return s[:100]
	}
	return s
}

// wave starts n calls evenly over ramp, each making one call, and returns their outcomes once every one has ended.
func (d *loadDriver) wave(ctx context.Context, n int, ramp time.Duration, spec func(i int) callSpec) []outcome {
	outs := make([]outcome, n)
	var wg sync.WaitGroup
	begin := time.Now()
	for i := range n {
		wg.Go(func() {
			sleepCtx(ctx, time.Until(begin.Add(ramp*time.Duration(i)/time.Duration(n))))
			outs[i] = d.run(ctx, spec(i))
		})
	}
	wg.Wait()
	return outs
}

// closed runs workers that each make one call after another until duration has passed, then lets the calls in flight end.
// A worker whose call is turned away waits a moment before it asks again, as a client does (an SDK waits longer).
func (d *loadDriver) closed(ctx context.Context, workers int, duration time.Duration, spec func(worker int) callSpec) []outcome {
	var mu sync.Mutex
	var all []outcome
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	for w := range workers {
		wg.Go(func() {
			var mine []outcome
			for time.Now().Before(deadline) && ctx.Err() == nil {
				o := d.run(ctx, spec(w))
				mine = append(mine, o)
				if o.refused() || o.status == 0 {
					sleepCtx(ctx, 200*time.Millisecond)
				}
			}
			mu.Lock()
			all = append(all, mine...)
			mu.Unlock()
		})
	}
	wg.Wait()
	return all
}

// flood sends calls with keys the node has never seen, as fast as workers can, until duration has passed, and counts
// the answers by status.
func (d *loadDriver) flood(ctx context.Context, workers int, duration time.Duration, key func() string) map[int]int {
	var mu sync.Mutex
	statuses := map[int]int{}
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	for range workers {
		wg.Go(func() {
			local := map[int]int{}
			for time.Now().Before(deadline) && ctx.Err() == nil {
				local[d.run(ctx, callSpec{client: sourceAddresses, key: key()}).status]++
			}
			mu.Lock()
			defer mu.Unlock()
			for status, n := range local {
				statuses[status] += n
			}
		})
	}
	wg.Wait()
	return statuses
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
