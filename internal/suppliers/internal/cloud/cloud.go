// Package cloud is what every cloud platform's model service shares (docs/domains/suppliers.md「云平台」): one is reached
// with a credential a person pastes, at hosts of the platform's own by region, and has no login, balance, offer or
// charge list to read. The platforms are adapters of their own, each in a package listed in the registry, so that
// removing one is deleting its package and its line; this package is not an adapter.
package cloud

import (
	"bytes"
	"context"
	"encoding/json/jsontext" //nolint:depguard // a body's top-level members are copied as written, without building a tree
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// Platform is the management half every cloud platform has in common: no rate buckets of the platform's that Axiom's
// requests share (each region's credential stands for its own quota, by its concurrency and per-minute ceiling), no
// detection, and nothing to log in to.
type Platform struct{}

// Buckets implements adapter.Adapter.
func (Platform) Buckets() []adapter.BucketSpec { return nil }

// Detect implements adapter.Adapter. A platform is chosen by name, never found by probing an address.
func (Platform) Detect(context.Context, adapter.Client) (adapter.Detection, error) {
	return adapter.Detection{}, adapter.ErrNotRecognised
}

// errNoManagement is Open's error: the sync never asks for a management side.
var errNoManagement = errors.New("cloud: a cloud platform has no management side to open")

// Open implements adapter.Adapter.
func (Platform) Open(context.Context, adapter.Client, adapter.LoginStore) (adapter.Manager, error) {
	return nil, errNoManagement
}

// Quirks is the call half every cloud platform has in common.
type Quirks struct{}

// Policy implements adapter.CallQuirks. A platform streams and stops generating when the client disconnects.
func (Quirks) Policy() adapter.CallPolicy { return adapter.CallPolicy{Streams: true, Cancels: true} }

// Formats implements adapter.CallQuirks. A cloud supplier's endpoints are written from their models' abilities when
// it is connected, not inferred from a group's listing.
func (Quirks) Formats(adapter.GroupFacts) []protocol.Format { return nil }

// ChargeRef implements adapter.CallQuirks. A platform's bill is not read, so no request id is kept for a charge that
// never comes.
func (Quirks) ChargeRef(http.Header) (adapter.ChargeRef, bool) { return "", false }

// Generates says m generates text, which every format a platform serves it in needs: it declares an output price and
// what serving reads.
func Generates(m catalog.Model) bool {
	_, serves := m.Serving()
	return serves && m.Reportable().Items.Has(catalog.ItemOutput)
}

// Member is a top-level member Rewrite adds: its name and its value as JSON.
type Member struct {
	Name  string
	Value []byte
}

// ErrNotObject is a body that is not one JSON object.
var ErrNotObject = errors.New("cloud: the body is not a JSON object")

// Rewrite is body with the top-level members named in drop left out and add appended, copied member by member as
// written, without building a tree; stream reports whether the body asks for a stream. A member of add that body has
// already is left out of body, so that the platform's own value is the one sent.
func Rewrite(body []byte, drop []string, add ...Member) (out []byte, stream bool, err error) {
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, false, ErrNotObject
	}
	out = make([]byte, 0, len(body)+64)
	out = append(out, '{')
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrNotObject, err)
		}
		name := tok.String()
		value, err := dec.ReadValue()
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrNotObject, err)
		}
		if name == "stream" {
			stream = string(value) == "true"
		}
		if dropped(name, drop, add) {
			continue
		}
		out = appendMember(out, name, value)
	}
	for _, m := range add {
		out = appendMember(out, m.Name, m.Value)
	}
	if len(out) > 1 && out[len(out)-1] == ',' {
		out = out[:len(out)-1]
	}
	return append(out, '}'), stream, nil
}

func dropped(name string, drop []string, add []Member) bool {
	for _, d := range drop {
		if d == name {
			return true
		}
	}
	for _, m := range add {
		if m.Name == name {
			return true
		}
	}
	return false
}

// appendMember appends "name":value, and a comma Rewrite takes off the last one.
func appendMember(out []byte, name string, value []byte) []byte {
	out, _ = jsontext.AppendQuote(out, name) //nolint:errcheck // name was read from valid JSON, so it quotes
	out = append(out, ':')
	return append(append(out, value...), ',')
}

const (
	// checkTimeout bounds a region's check, requests to a third party a person waits on.
	checkTimeout = 10 * time.Second
	// maxListing is the most of an answer to a check that is read: a platform's model listing is some hundreds of KiB.
	maxListing = 4 << 20
)

// Checked is a check's context: bounded by checkTimeout.
func Checked(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, checkTimeout)
}

// Fetch sends one request of a check and reads its answer, cut to maxListing. A request that got no answer is a check
// that says nothing about the credential: the CloudCheck to answer with is returned with the error.
func Fetch(ctx context.Context, d adapter.Doer, req *http.Request) (adapter.Response, adapter.CloudCheck, error) {
	resp, err := d.Do(req)
	if err != nil {
		check := adapter.CloudCheck{Cause: adapter.CauseTransport}
		if ctx.Err() != nil {
			check.Cause = adapter.CauseTimeout
		}
		return adapter.Response{}, check, err
	}
	defer resp.Body.Close()                                        //nolint:errcheck // the answer was read; closing it says nothing more
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListing)) // a listing longer than this is cut, and fails to parse
	if err != nil {
		return adapter.Response{}, adapter.CloudCheck{Status: resp.StatusCode, Cause: adapter.CauseTransport}, err
	}
	return adapter.Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, adapter.CloudCheck{Status: resp.StatusCode}, nil
}

// Judge is what a check's status says of the credential: 401 is it refused; 403 may be a permission or a block of the
// region and says nothing about the credential, as a rate limit, a timeout or a 5xx do not. A 2xx is passed only once
// the caller has read what it lists.
func Judge(status int) adapter.CloudCheck {
	c := adapter.CloudCheck{Status: status}
	switch {
	case status >= 200 && status < 300:
		c.Outcome = adapter.KeyPassed
	case status == http.StatusUnauthorized:
		c.Outcome, c.Cause = adapter.KeyFailed, adapter.CauseRejected
	case status == http.StatusForbidden:
		c.Cause = adapter.CauseNotPermitted
	case status == http.StatusTooManyRequests:
		c.Cause = adapter.CauseRateLimited
	case status >= http.StatusInternalServerError:
		c.Cause = adapter.CauseUpstreamError
	default:
		c.Cause = adapter.CauseUnknown
	}
	return c
}

// Rejected is the failure of a call the platform refused before running it because the credential is not valid: the
// region's credential is marked rejected (internal/suppliers/README.md「调用面」).
func Rejected() adapter.Failure {
	return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}
}

// Classify is how a platform's refusal of a call reads by its status alone, which each platform refines by its error
// body. 401 is the credential refused; 403 is the endpoint's (a model the account may not use, or a region's block),
// because treating it as the credential's would mark a working credential; 429 is this region's quota, so the region
// rests and routing moves on to another; 529 and 503 are no capacity.
func Classify(status int, h http.Header, body []byte) adapter.Failure {
	switch {
	case adapter.IsHTML(body): // a page is what a firewall or an anti-bot check answers, not the platform
		return adapter.Failure{Scope: adapter.ScopeEgress, Cause: adapter.Unknown{Status: status, SuspectedEgressBlock: true}}
	case status == http.StatusUnauthorized:
		return Rejected()
	case status == http.StatusTooManyRequests:
		return adapter.Failure{Scope: adapter.ScopeCredential, Cause: adapter.RateLimited{RetryAfter: adapter.RetryAfter(h)}}
	case status == http.StatusBadRequest, status == http.StatusRequestEntityTooLarge:
		return adapter.Failure{Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
	case status == http.StatusNotFound:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.ModelNotFound{}}
	case status == http.StatusForbidden:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.NotPermitted{}}
	case status == 529, status == http.StatusServiceUnavailable:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Overloaded{}}
	case status >= http.StatusInternalServerError:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: status}}
	}
	return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
}

// BadBody is the failure of a call whose body the platform's form cannot be made from.
func BadBody() *adapter.Failure {
	return &adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeRequest, Cause: adapter.BadRequest{}}
}

// Unsigned is the failure of a call whose authorization could not be had for a reason that says nothing about the
// credential (the token service did not answer): the region rests, and nothing reached the platform.
func Unsigned() *adapter.Failure {
	return &adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.Transport{}}
}

// Post is a JSON POST of body to url in ctx.
func Post(ctx context.Context, url string, body []byte, h http.Header) (*http.Request, *adapter.Failure) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, BadBody()
	}
	maps.Copy(req.Header, h)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}
