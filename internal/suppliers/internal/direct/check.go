package direct

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

const (
	// checkTimeout bounds one key's check, a request to a third party that a person waits on.
	checkTimeout = 10 * time.Second
	// maxModels is the most names one listing is read for; a provider lists a few hundred at most.
	maxModels = 2000
	// maxName is the longest model name kept; a longer one is not a name a call would send.
	maxName = 200
)

// models is the model list every family answers with: {"data":[{"id":…}]}. Anthropic pages it, and one page of its
// largest size holds its whole list.
type models struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// CheckKey implements adapter.Direct: one GET of the model list, which no provider charges for. Only a 401, or
// Google's 400 that says the key is not valid, fails the key: a 403 may be a region block, and a rate limit, a timeout
// or a 5xx says nothing about it. The response body is read for the names only; nothing of it is kept besides them.
func (a *Adapter) CheckKey(ctx context.Context, c adapter.Client, f adapter.Family, key secret.Secret, s adapter.Settings) adapter.KeyCheck {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	h := http.Header{"Accept": {"application/json"}}
	path, format := a.p.Models, protocol.Chat
	if f == adapter.FamilyAnthropic {
		path, format = "/v1/models?limit=1000", protocol.Messages
		h.Set("Anthropic-Version", "2023-06-01")
	}
	quirks{a.p}.Authorize(h, format, key.Reveal(), s)
	resp, err := c.Do(ctx, adapter.Request{Method: http.MethodGet, Path: path, Header: h})
	switch {
	case errors.Is(err, adapter.ErrBucketsFull):
		return adapter.KeyCheck{Cause: adapter.CauseRateLimited, Status: http.StatusTooManyRequests}
	case errors.Is(err, context.DeadlineExceeded):
		return adapter.KeyCheck{Cause: adapter.CauseTimeout}
	case err != nil:
		return adapter.KeyCheck{Cause: adapter.CauseTransport}
	}
	return checked(resp)
}

// errNoBalance is ReadBalance's for a provider that tells no balance; its type does not say Balance, so it is not asked.
var errNoBalance = errors.New("direct: the provider tells no balance")

// ReadBalance implements adapter.Balancer: one GET of the provider's balance, which it does not charge for, with the key
// as a Chat call takes it. An answer other than 200, or one Read cannot read, is an error that holds no part of it.
func (a *Adapter) ReadBalance(ctx context.Context, c adapter.Client, key secret.Secret, s adapter.Settings, currency platform.Currency) (adapter.Money, error) {
	if a.p.Balance == nil {
		return adapter.Money{}, errNoBalance
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	h := http.Header{"Accept": {"application/json"}}
	quirks{a.p}.Authorize(h, protocol.Chat, key.Reveal(), s)
	resp, err := c.Do(ctx, adapter.Request{Method: http.MethodGet, Path: a.p.Balance.Path, Header: h})
	if err != nil {
		return adapter.Money{}, fmt.Errorf("direct: read the balance: %w", err)
	}
	if resp.Status != http.StatusOK {
		return adapter.Money{}, fmt.Errorf("direct: the balance answered %d", resp.Status)
	}
	m, err := a.p.Balance.Read(resp.Body, currency)
	if err != nil {
		return adapter.Money{}, fmt.Errorf("direct: read the balance's answer: %w", err)
	}
	return m, nil
}

// checked reads the answer to a check.
func checked(resp adapter.Response) adapter.KeyCheck {
	out := adapter.KeyCheck{Status: resp.Status}
	switch {
	case resp.Status == http.StatusOK:
	case resp.Status == http.StatusUnauthorized, resp.Status == http.StatusBadRequest && strings.Contains(string(resp.Body), string(invalidKey)):
		out.Outcome, out.Cause = adapter.KeyFailed, adapter.CauseRejected
		return out
	case resp.Status == http.StatusForbidden:
		out.Cause = adapter.CauseNotPermitted
		return out
	case resp.Status >= 500:
		out.Cause = adapter.CauseUpstreamError
		return out
	default:
		out.Cause = adapter.CauseUnknown
		return out
	}
	names, ok := modelNames(resp.Body)
	if !ok {
		out.Cause = adapter.CauseUnknown // the key was let in, but the answer is not a list to serve from
		return out
	}
	out.Outcome, out.Models = adapter.KeyPassed, names
	return out
}

// modelNames is the names a list holds, sorted and once each. Google names its models "models/<name>" in the list and
// takes the bare name in a call. A name with a space or a control character, or longer than any a call sends, is left
// out; a list that is not one is not ok.
func modelNames(body []byte) ([]string, bool) {
	var list models
	if err := jsonx.Unmarshal(body, &list); err != nil || list.Data == nil {
		return nil, false
	}
	names := make([]string, 0, min(len(list.Data), maxModels))
	for _, m := range list.Data[:min(len(list.Data), maxModels)] {
		name := strings.TrimPrefix(m.ID, "models/")
		if name == "" || len(name) > maxName || strings.ContainsFunc(name, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return slices.Compact(names), true
}
