package main

import (
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/jsonx"
)

type managedInput struct {
	Name        jsonx.Raw `json:"name"`
	Disabled    jsonx.Raw `json:"disabled"`
	Limit       jsonx.Raw `json:"limit"`
	Reset       jsonx.Raw `json:"limit_reset"`
	Expires     jsonx.Raw `json:"expires_at"`
	Models      jsonx.Raw `json:"models"`
	IPs         jsonx.Raw `json:"ip_allow"`
	Workspace   *string   `json:"workspace_id"`
	Creator     *string   `json:"creator_user_id"`
	IncludeBYOK *bool     `json:"include_byok_in_limit"`
}

func readManagementInput(r *http.Request) (managedInput, error) {
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		return managedInput{}, nil
	}
	return managementInput(r, r.Method == http.MethodPost)
}

func managementInput(r *http.Request, create bool) (managedInput, error) {
	var in managedInput
	body, err := io.ReadAll(r.Body)
	if err != nil || jsonx.UnmarshalKnown(body, &in) != nil {
		return in, errManagementInput
	}
	if create {
		if len(in.Name) == 0 || len(in.Disabled) != 0 {
			return in, errManagementInput
		}
	} else {
		if len(in.Expires) != 0 || in.Workspace != nil || in.Creator != nil {
			return in, errManagementInput
		}
		if len(in.Name)+len(in.Disabled)+len(in.Limit)+len(in.Reset)+len(in.Models)+len(in.IPs) == 0 && in.IncludeBYOK == nil {
			return in, errManagementInput
		}
	}
	return in, nil
}

// settings preserves omission and null separately: PATCH omission keeps a value, null clears nullable restrictions.
func (in managedInput) settings(k accounts.KeySettings, expiry *time.Time, create bool) (accounts.KeySettings, *time.Time, error) {
	if create {
		k.Disabled = false
	}
	if err := in.names(&k); err != nil {
		return k, nil, err
	}
	if err := in.spend(&k); err != nil {
		return k, nil, err
	}
	if err := in.addresses(&k); err != nil {
		return k, nil, err
	}
	expiry, err := in.expiry(expiry)
	return k, expiry, err
}

func (in managedInput) names(k *accounts.KeySettings) error {
	for _, field := range []struct {
		raw    jsonx.Raw
		target any
	}{
		{in.Name, &k.Name}, {in.Disabled, &k.Disabled}, {in.Models, &k.Models},
	} {
		if len(field.raw) > 0 && jsonx.Unmarshal(field.raw, field.target) != nil {
			return errManagementInput
		}
	}
	if string(in.Name) == "null" || string(in.Disabled) == "null" || utf8.RuneCountInString(k.Name) < 1 || utf8.RuneCountInString(k.Name) > 100 {
		return errManagementInput
	}
	return nil
}

func (in managedInput) expiry(inherited *time.Time) (*time.Time, error) {
	if len(in.Expires) == 0 {
		return inherited, nil
	}
	if string(in.Expires) == "null" {
		return nil, accounts.ErrForbidden
	}
	var timestamp string
	if jsonx.Unmarshal(in.Expires, &timestamp) != nil || !strings.HasSuffix(timestamp, "Z") {
		return nil, errManagementInput
	}
	at, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil || !at.After(time.Now()) {
		return nil, errManagementInput
	}
	return &at, nil
}

func (in managedInput) spend(k *accounts.KeySettings) error {
	if len(in.Limit) > 0 {
		if string(in.Limit) == "null" {
			k.SpendLimit = nil
		} else {
			amount, err := managementAmount(in.Limit)
			if err != nil {
				return err
			}
			k.SpendLimit = &amount
		}
	}
	reset, err := in.reset(k.LimitReset)
	if err != nil {
		return err
	}
	k.LimitReset = reset
	if k.LimitReset != "" && k.SpendLimit == nil {
		return errManagementInput
	}

	return nil
}

func (in managedInput) reset(previous string) (string, error) {
	if len(in.Reset) == 0 {
		return previous, nil
	}
	if string(in.Reset) == "null" {
		return "", nil
	}
	var reset string
	if jsonx.Unmarshal(in.Reset, &reset) != nil || !slices.Contains([]string{accounts.ResetDaily, accounts.ResetWeekly, accounts.ResetMonthly}, reset) {
		return "", errManagementInput
	}
	return reset, nil
}

func (in managedInput) addresses(k *accounts.KeySettings) error {
	if len(k.Models) > 256 {
		return errManagementInput
	}
	for _, model := range k.Models {
		if len(model) == 0 || utf8.RuneCountInString(model) > 200 {
			return errManagementInput
		}
	}
	if len(in.IPs) > 0 {
		var ips []string
		if jsonx.Unmarshal(in.IPs, &ips) != nil || len(ips) > 64 {
			return errManagementInput
		}
		var problems problems
		k.IPAllow = networks(ips, &problems)
		if len(problems) > 0 {
			return errManagementInput
		}
	}
	return nil
}

// Decimal JSON limits must fit exactly in billionths. Bound the exponent before big.Rat can allocate for it.
func managementAmount(raw jsonx.Raw) (int64, error) {
	s := string(raw)
	if len(s) > 64 || len(s) == 0 || s[0] < '0' || s[0] > '9' {
		return 0, errManagementInput
	}
	if at := strings.IndexAny(s, "eE"); at >= 0 {
		exponent, err := strconv.ParseInt(s[at+1:], 10, 8)
		if err != nil || exponent < -18 || exponent > 18 {
			return 0, errManagementInput
		}
	}
	n, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, errManagementInput
	}
	n.Mul(n, big.NewRat(1e9, 1))
	if !n.IsInt() || !n.Num().IsInt64() {
		return 0, errManagementInput
	}
	return n.Num().Int64(), nil
}

func managedPage(q url.Values) (accounts.ManagedKeyPage, error) {
	var page accounts.ManagedKeyPage
	for key, values := range q {
		if len(values) != 1 {
			return page, errManagementInput
		}
		var err error
		switch key {
		case "offset":
			page.Offset, err = strconv.ParseInt(values[0], 10, 64)
			if page.Offset < 0 {
				return page, errManagementInput
			}
		case "include_disabled":
			page.IncludeDisabled, err = strconv.ParseBool(values[0])
		case "include_expired":
			page.IncludeExpired, err = strconv.ParseBool(values[0])
		case "workspace_id":
			id, parseErr := uuid.Parse(values[0])
			err = parseErr
			workspace := accounts.WorkspaceID(id)
			page.Workspace = &workspace
		default:
			return page, errManagementInput
		}
		if err != nil {
			return page, errManagementInput
		}
	}
	return page, nil
}
