package main

import (
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"time"
	"uuid"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"axiom/internal/accounts"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
)

// problems are the parts of a request that break what the contract's schemas cannot say: a rule across fields, or
// one the table would refuse. They answer as a 400 naming each.
type problems []errorField

func (p *problems) add(pointer, message string) {
	*p = append(*p, errorField{Pointer: pointer, Message: message})
}

func (p *problems) err() error {
	if len(*p) == 0 {
		return nil
	}
	return &apiError{status: http.StatusBadRequest, code: codeInvalidRequest, message: "the request breaks the contract", fields: *p}
}

// keyInput is the settings of a key as KeyCreate and KeySettings both give them; expires only a new key has.
type keyInput struct {
	name     string
	disabled bool
	limit    *consoleapi.Money
	reset    *consoleapi.LimitReset
	models   *[]string
	ips      *[]string
	preset   *openapi_types.UUID
	expires  *time.Time
}

func createInput(b *consoleapi.KeyCreate) keyInput {
	return keyInput{name: b.Name, limit: b.SpendLimit, reset: b.LimitReset, models: b.Models, ips: b.IpAllow, preset: b.PresetId, expires: b.ExpiresAt}
}

func updateInput(b *consoleapi.KeySettings) keyInput {
	return keyInput{name: b.Name, disabled: b.Disabled, limit: b.SpendLimit, reset: b.LimitReset, models: b.Models, ips: b.IpAllow, preset: b.PresetId}
}

// settings checks the input against what the table takes and turns it into the module's settings and the expiry: money
// in the site's currency and not negative, a reset only with a limit, no empty model name, addresses that parse (a
// network is taken as its masked form, the table's cidr type allows no host bits), and an expiry after now, in UTC.
func (in keyInput) settings(currency platform.Currency, now time.Time) (accounts.KeySettings, *time.Time, error) {
	var p problems
	s := accounts.KeySettings{Name: in.name, Disabled: in.disabled, SpendLimit: spendLimit(in.limit, currency, "/spend_limit", &p), Preset: (*uuid.UUID)(in.preset)}
	var expires *time.Time
	if in.expires != nil {
		if !in.expires.After(now) {
			p.add("/expires_at", "must be in the future")
		}
		utc := in.expires.UTC()
		expires = &utc
	}
	if in.reset != nil {
		if s.SpendLimit == nil && in.limit == nil {
			p.add("/limit_reset", "needs a spend_limit")
		}
		s.LimitReset = string(*in.reset)
	}
	if in.models != nil {
		s.Models = *in.models
		for i, model := range s.Models {
			if model == "" {
				p.add(fmt.Sprintf("/models/%d", i), "must not be empty")
			}
		}
	}
	if in.ips != nil {
		s.IPAllow = networks(*in.ips, &p)
	}
	return s, expires, p.err()
}

// spendLimit is the amount in billionths of the limit at pointer, a key's spend limit or a workspace's budget, when it
// is money of the site's currency and not negative.
func spendLimit(m *consoleapi.Money, currency platform.Currency, pointer string, p *problems) *int64 {
	if m == nil {
		return nil
	}
	if m.Currency != currency.String() {
		p.add(pointer+"/currency", "the site's currency is "+currency.String())
		return nil
	}
	n, err := strconv.ParseInt(m.Amount, 10, 64)
	if err != nil || n < 0 {
		p.add(pointer+"/amount", "must be a whole number of billionths, not negative, that fits in 63 bits")
		return nil
	}
	return &n
}

// networks are the entries of ip_allow as networks: an address is its own network.
func networks(entries []string, p *problems) []netip.Prefix {
	var out []netip.Prefix
	for i, entry := range entries {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, addrErr := netip.ParseAddr(entry)
			if addrErr != nil || addr.Zone() != "" {
				p.add(fmt.Sprintf("/ip_allow/%d", i), "not an IP address or a network such as 203.0.113.0/24")
				continue
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, prefix.Masked())
	}
	return out
}
