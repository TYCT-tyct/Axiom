package payments

import (
	"fmt"
	"net/url"
	"strings"
)

// linkSchemes are the place a checkout may send the browser: an https address, or one of the wallets' own apps
// (README.md「渠道接口」). A protocol-relative address has no scheme and is refused with the rest.
var linkSchemes = map[string]bool{"https": true, "alipays": true, "weixin": true, "wxp": true}

// withoutNUL is c without the NUL bytes a gateway's answer may carry: they mean nothing in a link, and the database
// refuses them (sub2api backend/internal/service/payment_order.go:509-523 at 8f2cbb5).
func (c Checkout) withoutNUL() Checkout {
	clean := func(s string) string { return strings.ReplaceAll(s, "\x00", "") }
	return Checkout{TradeNo: clean(c.TradeNo), PayURL: clean(c.PayURL), QRCode: clean(c.QRCode), ClientSecret: clean(c.ClientSecret)}
}

// stored is what outlives the request: the client secret goes to the browser only, never to the database.
func (c Checkout) stored() Checkout {
	c.ClientSecret = ""
	return c
}

// check refuses a link the browser must not follow. The channel hands out absolute links; a relative one is a plugin's
// mistake, since completing it is the plugin's work, and is refused here like any other scheme.
func (c Checkout) check() error {
	for _, link := range []string{c.PayURL, c.QRCode} {
		if link == "" {
			continue
		}
		u, err := url.Parse(link)
		if err != nil || !linkSchemes[u.Scheme] || u.Host == "" {
			return fmt.Errorf("%w: %s", ErrUnsafeLink, schemeOf(link))
		}
	}
	return nil
}

// schemeOf names what a refused link is without repeating it: its query may hold the merchant's key.
func schemeOf(link string) string {
	if i := strings.IndexByte(link, ':'); i > 0 {
		return link[:i]
	}
	return "no scheme"
}
