package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Client is how an adapter reaches the supplier. It is the only way: the module's shared code implements it over the
// egress and the rate limits, so that an adapter that sends only through it cannot send a request without taking its
// allowance.
type Client interface {
	// Do sends r once the supplier's limits allow every bucket r names, and takes one request from each. When any
	// of them has no allowance left it sends nothing and returns an error that wraps ErrBucketsFull, so the caller
	// defers the work instead of failing it.
	Do(ctx context.Context, r Request) (Response, error)
}

// ErrBucketsFull is the error of a request that was not sent because a bucket had no allowance left.
var ErrBucketsFull = errors.New("adapter: a rate bucket has no allowance left")

// Request is one request to the supplier. Path is relative to the supplier's origin, which is fixed at connection.
// Buckets names every bucket the request counts against: the supplier counts one request in several buckets at once
// (new-api puts every /api/ path in a global bucket and its sensitive ones in a second).
type Request struct {
	Method, Path string
	Header       http.Header
	Body         []byte
	Buckets      []string
}

// Response is the supplier's answer, its body cut to a limit.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Counted says what a supplier counts requests per.
type Counted uint8

// The things a limit counts per.
const (
	CountedByIP Counted = iota
	CountedByAccount
)

// BucketSpec is one rate limit of the supplier: what it counts per, over what window, and how many requests we allow
// ourselves, which is a fraction of what the supplier allows, so that other users of the same address cannot push us
// over it.
type BucketSpec struct {
	Name   string
	Per    Counted
	Window time.Duration
	Limit  int
}

// ValidateBuckets reports a declaration that cannot work: a bucket with no name, window or limit, or two with one name.
func ValidateBuckets(specs []BucketSpec) error {
	seen := make(map[string]bool, len(specs))
	for _, b := range specs {
		if b.Name == "" || b.Window <= 0 || b.Limit <= 0 {
			return fmt.Errorf("bucket %q needs a name, a positive window and a positive limit", b.Name)
		}
		if seen[b.Name] {
			return fmt.Errorf("bucket %q: declared twice", b.Name)
		}
		seen[b.Name] = true
	}
	return nil
}
