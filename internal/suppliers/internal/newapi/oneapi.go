package newapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// What one-api (v0.6.10, songquanpeng/one-api) answers differently from new-api, read from its source at that tag. It
// has no user-visible groups or ratios, no price list and no subscriptions; its listings are pages of a fixed ten
// (config.ItemsPerPage) counted from 0 and answered as bare lists without a total; a key is listed and read with its
// whole text, has no group of its own (it follows the account's) and limits models by a comma list.

// oneAPIPageSize is one-api's page size, which a request cannot change.
const oneAPIPageSize = 10

func (m *manager) keyPageSize() int {
	if m.sys == oneAPI {
		return oneAPIPageSize
	}
	return keyPageSize
}

func (m *manager) logPageSize() int {
	if m.sys == oneAPI {
		return oneAPIPageSize
	}
	return logPageSize
}

// oneAPIGroup is the account's one group (/api/user/self).
func (m *manager) oneAPIGroup(ctx context.Context) (string, error) {
	var self struct {
		Data *struct {
			Group string `json:"group"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, "/api/user/self", global, &self); err != nil {
		return "", m.selfRefused(err)
	}
	if self.Data == nil || self.Data.Group == "" {
		return "", errMissing("group of the account")
	}
	return self.Data.Group, nil
}

// oneAPIGroups is the account's one group. one-api shows an ordinary user neither the group's ratio nor any other
// group, so the ratio is written as 1 and marked hidden: a base price a person confirms for it has to include the
// site's ratio already.
func (m *manager) oneAPIGroups(ctx context.Context) (map[string]groupEntry, error) {
	group, err := m.oneAPIGroup(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]groupEntry{group: {Ratio: "1", Hidden: true}}, nil
}

// oneAPIKeyPage is page (from 1) of the account's keys, each in the account's group, with its text masked as new-api
// masks it, so that FindKey compares the same way; the whole text is not kept.
func (m *manager) oneAPIKeyPage(ctx context.Context, page int) ([]token, int, error) {
	group, err := m.oneAPIGroup(ctx)
	if err != nil {
		return nil, 0, err
	}
	var list struct {
		Data []struct {
			ID      int64   `json:"id"`
			Name    string  `json:"name"`
			Key     string  `json:"key"`
			Status  int     `json:"status"`
			Expires *int64  `json:"expired_time"`
			Models  *string `json:"models"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, "/api/token/?p="+strconv.Itoa(page-1), global, &list); err != nil {
		return nil, 0, err
	}
	if list.Data == nil {
		return nil, 0, errMissing("list of keys")
	}
	keys := make([]token, 0, len(list.Data))
	for _, item := range list.Data {
		status, ok := keyStatus[item.Status]
		if !ok || item.ID <= 0 {
			return nil, 0, fmt.Errorf("%w: a key with status %d", adapter.ErrUnrecognised, item.Status)
		}
		limited := item.Models != nil && *item.Models != "" // one-api limits a key's models only by a list that is not empty
		policy, err := keyPolicy(item.Expires, &limited, item.Models)
		if err != nil {
			return nil, 0, err
		}
		keys = append(keys, token{state: adapter.KeyState{
			Ref: adapter.KeyRef(strconv.FormatInt(item.ID, 10)), GroupRef: group, Name: item.Name, Status: status, Policy: policy,
		}, masked: maskKey(item.Key)})
	}
	return keys, 0, nil
}

// oneAPIKeySecret reads a key's text with the key (GET /api/token/:id), which one-api counts in its global bucket only.
func (m *manager) oneAPIKeySecret(ctx context.Context, ref adapter.KeyRef) (secret.Secret, error) {
	var read struct {
		Data *struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, "/api/token/"+string(ref), global, &read); err != nil {
		return secret.Secret{}, err
	}
	if read.Data == nil || read.Data.Key == "" {
		return secret.Secret{}, errMissing("key text")
	}
	return secret.New("sk-" + strings.TrimPrefix(read.Data.Key, "sk-")), nil
}

// oneAPIKeyCreated is the id of the key one-api answered an add with. An answer without one leaves the key to be
// adopted by its name on the next complete listing.
func oneAPIKeyCreated(resp adapter.Response) (adapter.KeyRef, error) {
	var made struct {
		Data *struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if jsonx.Unmarshal(resp.Body, &made) != nil || made.Data == nil || made.Data.ID <= 0 {
		return "", fmt.Errorf("new-api: the key made has no id in the answer")
	}
	return adapter.KeyRef(strconv.FormatInt(made.Data.ID, 10)), nil
}

// oneAPILogPage is page p.Page (from 1) of the pass's consumption records.
func (m *manager) oneAPILogPage(ctx context.Context, p *pass) (logPage, error) {
	var list struct {
		Data []logItem `json:"data"`
	}
	path := "/api/log/self?type=2&p=" + strconv.Itoa(p.Page-1) +
		"&start_timestamp=" + strconv.FormatInt(p.Start, 10) + "&end_timestamp=" + strconv.FormatInt(p.End, 10)
	if err := m.api.do(ctx, http.MethodGet, path, global, &list); err != nil {
		return logPage{}, err
	}
	page := logPage{uncounted: true}
	if list.Data != nil {
		page.Data = &logData{Items: list.Data}
	}
	return page, nil
}
