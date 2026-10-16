package gateway

import (
	"bytes"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// override reads the request's provider object into what routing narrows the route by. Every member either becomes
// part of the override or is refused 400 by name: a condition the route does not keep is one the client believes it
// has, so it is never treated as not written. What is refused is what needs data this node does not have yet: the
// names of suppliers (docs/domains/gateway.md「路由参数」) and the options routing has no filter for. A member that
// asks for what the default already gives (an empty list, allow_fallbacks true) is accepted.
func override(raw []byte) (routing.Override, error) {
	var o routing.Override
	if raw == nil {
		return o, nil
	}
	var members map[string]jsonx.Raw
	if err := jsonx.Unmarshal(raw, &members); err != nil {
		return o, invalid("provider is not valid.")
	}
	for _, name := range slices.Sorted(maps.Keys(members)) { // in a fixed order, so a request with two faults is told the same one
		value := bytes.TrimSpace(members[name])
		if string(value) == "null" { // as not written
			continue
		}
		read, known := providerMembers[name]
		if !known {
			return o, unsupported(name)
		}
		if err := read(&o, name, value); err != nil {
			return o, err
		}
	}
	return o, nil
}

// providerMembers reads each member of the provider object. The names are OpenRouter's (OpenRouter《Provider Routing》,
// 2026-10); docs/domains/routing.md「请求覆盖」 says what each does.
var providerMembers = map[string]func(o *routing.Override, name string, value []byte) error{
	"sort":            readSort,
	"data_collection": readDataCollection,
	"zdr":             readZDR,
	"max_price":       readMaxPrice,
	"preferred_min_throughput": func(o *routing.Override, name string, v []byte) (err error) {
		o.MinThroughput, err = jsonNumber(name, v, 1e9)
		return err
	},
	"preferred_max_latency": readMaxLatency,
	// The suppliers a request names are addressed by name or code name, and nothing the gateway can reach maps those to
	// the supplier IDs routing filters by: an empty list is the default, a name is refused.
	"order":  noNames,
	"only":   noNames,
	"ignore": noNames,
	// allow_fallbacks false means only the first of order is tried, and there is no order here; require_parameters
	// true asks for endpoints that support every parameter written, which routing does not know of parameters beyond
	// the capabilities it already requires.
	"allow_fallbacks":    func(_ *routing.Override, name string, v []byte) error { return mustBe(name, v, true) },
	"require_parameters": func(_ *routing.Override, name string, v []byte) error { return mustBe(name, v, false) },
}

func unsupported(name string) *Refusal { return invalid("provider." + name + " is not supported yet.") }

func mismatch(name, want string) *Refusal {
	return invalid("provider." + name + " must be " + want + ".")
}

// mustBe accepts the boolean that is the default and refuses the other.
func mustBe(name string, value []byte, allowed bool) error {
	var b bool
	if err := jsonx.Unmarshal(value, &b); err != nil {
		return mismatch(name, "true or false")
	}
	if b != allowed {
		return unsupported(name + " " + strconv.FormatBool(b))
	}
	return nil
}

func noNames(_ *routing.Override, name string, value []byte) error {
	var names []string
	if err := jsonx.Unmarshal(value, &names); err != nil {
		return mismatch(name, "a list of names")
	}
	if len(names) > 0 {
		return unsupported(name)
	}
	return nil
}

// strategies are the values of sort that routing has a ranking for. Its Fastest ranks by throughput and only then by time
// to first token, so it is not a ranking by latency: sort latency is refused rather than served as throughput.
var strategies = map[string]routing.Strategy{"price": routing.Cheapest, "throughput": routing.Fastest}

func readSort(o *routing.Override, name string, value []byte) error {
	var by string
	if err := jsonx.Unmarshal(value, &by); err != nil {
		return mismatch(name, "price or throughput")
	}
	if by == "latency" {
		return unsupported(name + " latency")
	}
	s, ok := strategies[by]
	if !ok {
		return mismatch(name, "price or throughput")
	}
	o.Sort = &s
	return nil
}

// noRetention is what both data_collection deny and zdr ask for: endpoints whose supplier does not keep the data or
// train on it. Retained data is kept, so it does not pass either.
const noRetention = routing.PolicySet(1 << routing.PolicyNoRetention)

func readDataCollection(o *routing.Override, name string, value []byte) error {
	var v string
	if err := jsonx.Unmarshal(value, &v); err != nil || v != "allow" && v != "deny" {
		return mismatch(name, "allow or deny")
	}
	if v == "deny" {
		o.Policies = noRetention
	}
	return nil
}

func readZDR(o *routing.Override, name string, value []byte) error {
	var v bool
	if err := jsonx.Unmarshal(value, &v); err != nil {
		return mismatch(name, "true or false")
	}
	if v {
		o.Policies = noRetention
	}
	return nil
}

// jsonNumber reads a JSON number from 0 to hi.
func jsonNumber(name string, value []byte, hi float64) (float64, error) {
	var v float64
	if err := jsonx.Unmarshal(value, &v); err != nil || v < 0 || v > hi {
		return 0, mismatch(name, "a number (percentile objects are not supported yet)")
	}
	return v, nil
}

// readMaxLatency reads seconds, as OpenRouter does.
func readMaxLatency(o *routing.Override, name string, value []byte) error {
	seconds, err := jsonNumber(name, value, 1e6)
	o.MaxLatency = time.Duration(seconds * float64(time.Second))
	return err
}

// maxPriceItems are the members of max_price that routing can hold a price to: the price of a prompt token and of a
// completion token. image, audio and request are other billing items whose limits routing does not compare.
var maxPriceItems = map[string]catalog.Item{"prompt": catalog.ItemInput, "completion": catalog.ItemOutput}

// readMaxPrice reads max_price, in credits of the site per million tokens like the prices of the model list: the
// site's currency is its unit of account (docs/domains/billing.md「钱的表示」), which for the main site is OpenRouter's
// dollar. A limit that is not a whole multiple of the price unit is refused, not rounded either way.
func readMaxPrice(o *routing.Override, name string, value []byte) error {
	var members map[string]jsonx.Raw
	if err := jsonx.Unmarshal(value, &members); err != nil {
		return mismatch(name, "an object")
	}
	for _, member := range slices.Sorted(maps.Keys(members)) {
		v := bytes.TrimSpace(members[member])
		if string(v) == "null" {
			continue
		}
		item, ok := maxPriceItems[member]
		if !ok {
			return unsupported(name + "." + member)
		}
		price, ok := perToken(v)
		if !ok {
			return mismatch(name+"."+member, "a number of credits per million tokens with at most 9 decimals, from 0 to 1000000000")
		}
		o.MaxPrice.Set.Add(item)
		o.MaxPrice.Max[item] = price
	}
	return nil
}

// priceNumber is a plain JSON number of at most a three-digit exponent: digits, an optional fraction, an optional
// exponent. Nothing else is a price, and the exponent's size is bounded here, before any arithmetic.
var priceNumber = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]{1,3}))?$`)

// perToken turns a JSON number of credits per million tokens into the price of one token. The number is read as exact
// decimal digits and a power of ten, never through a float, so the limit is exactly what was written. A unit price
// counts 10⁻¹⁵ of a credit and a million tokens make 10⁻⁶ of that, so the price is the number times 10⁹ units, which
// has to be a whole number and at most 10¹⁸ (UnitPrice's limit). The digits and the power are checked to be within
// those bounds before the big integer is made, so what an exponent such as 1e1000000 asks for is never computed.
func perToken(number []byte) (platform.UnitPrice, bool) {
	m := priceNumber.FindSubmatch(number)
	if m == nil {
		return platform.UnitPrice{}, false
	}
	exp := 0
	if len(m[3]) > 0 {
		exp, _ = strconv.Atoi(string(m[3])) //nolint:errcheck // at most a sign and three digits
	}
	digits := strings.TrimLeft(string(m[1])+string(m[2]), "0")
	scale := exp - len(m[2]) // the number is digits × 10^scale
	if trimmed := strings.TrimRight(digits, "0"); digits != "" {
		scale += len(digits) - len(trimmed)
		digits = trimmed
	}
	if digits == "" {
		return platform.UnitPrice{}, true // the zero value is the price of nothing
	}
	shift := scale + 9
	if len(digits) > 19 || shift < 0 || shift+len(digits) > 19 {
		return platform.UnitPrice{}, false
	}
	units, _ := new(big.Int).SetString(digits, 10)
	units.Mul(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(shift)), nil))
	whole, frac := new(big.Int).QuoRem(units, big.NewInt(1e15), new(big.Int))
	fraction := frac.String()
	price, err := platform.ParseUnitPrice(whole.String() + "." + strings.Repeat("0", 15-len(fraction)) + fraction)
	return price, err == nil
}
