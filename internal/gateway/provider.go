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
	"axiom/internal/suppliers"
)

// override validates the provider object before admission. A constraint is either enforced by the route or refused
// by name; accepting a condition this node cannot keep would give the client a false guarantee.
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
	"sort":                     readSort,
	"data_collection":          readDataCollection,
	"zdr":                      readZDR,
	"max_price":                readMaxPrice,
	"preferred_min_throughput": readMinThroughput,
	"preferred_max_latency":    readMaxLatency,
	"order":                    readCodenames,
	"only":                     readCodenames,
	"ignore":                   readCodenames,
	"quantizations":            readQuantizations,
	"allow_fallbacks": func(o *routing.Override, name string, v []byte) error {
		var allow bool
		if err := jsonx.Unmarshal(v, &allow); err != nil {
			return mismatch(name, "true or false")
		}
		o.NoFallbacks = !allow
		return nil
	},
	"require_parameters": readRequireParameters,
}

func unsupported(name string) *Refusal {
	return invalid("provider." + name + " is not supported yet.")
}

func mismatch(name, want string) *Refusal {
	return invalid("provider." + name + " must be " + want + ".")
}

func readRequireParameters(o *routing.Override, name string, value []byte) error {
	var b bool
	if err := jsonx.Unmarshal(value, &b); err != nil {
		return mismatch(name, "true or false")
	}
	o.RequireParameters = b
	return nil
}

func readQuantizations(o *routing.Override, name string, value []byte) error {
	var names []string
	if err := jsonx.Unmarshal(value, &names); err != nil {
		return mismatch(name, "a list of OpenRouter's quantization levels")
	}
	levels := make([]suppliers.Quantization, 0, len(names))
	for _, levelName := range names {
		level, err := suppliers.ParseQuantization(levelName)
		if err != nil {
			return mismatch(name, "a list of OpenRouter's quantization levels")
		}
		if !slices.Contains(levels, level) {
			levels = append(levels, level)
		}
	}
	if len(levels) > 0 {
		o.Quantizations = levels
	}
	return nil
}

func readCodenames(o *routing.Override, name string, value []byte) error {
	var names []string
	if err := jsonx.Unmarshal(value, &names); err != nil {
		return mismatch(name, "a list of names")
	}
	if len(names) == 0 {
		return nil
	}
	if slices.Contains(names, "") {
		return mismatch(name, "a list of nonempty public group codenames")
	}
	switch name {
	case "order":
		o.OrderNames = names
	case "only":
		o.OnlyNames = names
	case "ignore":
		o.ExcludeNames = names
	}
	return nil
}

var strategies = map[string]routing.Strategy{
	"price": routing.Cheapest, "throughput": routing.Fastest, "latency": routing.LowestLatency,
}

func readSort(o *routing.Override, name string, value []byte) error {
	var by string
	if len(value) > 0 && value[0] == '{' {
		var err error
		by, err = readSortObject(o, name, value)
		if err != nil {
			return err
		}
	} else if err := jsonx.Unmarshal(value, &by); err != nil {
		return mismatch(name, "price, throughput, latency or a sort object")
	}
	s, ok := strategies[by]
	if !ok {
		return mismatch(name, "price, throughput or latency")
	}
	o.Sort = &s
	return nil
}

func readSortObject(o *routing.Override, name string, value []byte) (string, error) {
	var members map[string]jsonx.Raw
	if err := jsonx.Unmarshal(value, &members); err != nil {
		return "", mismatch(name, "a sort object")
	}
	var by string
	for _, member := range slices.Sorted(maps.Keys(members)) {
		v := bytes.TrimSpace(members[member])
		if string(v) == "null" {
			continue
		}
		if err := readSortMember(o, name, member, v, &by); err != nil {
			return "", err
		}
	}
	return by, nil
}

// readSortMember reads one member of a sort object: the ordering and the partition it applies to.
func readSortMember(o *routing.Override, name, member string, value []byte, by *string) error {
	switch member {
	case "by":
		if err := jsonx.Unmarshal(value, by); err != nil {
			return mismatch(name+".by", "price, throughput or latency")
		}
	case "partition":
		var partition string
		if err := jsonx.Unmarshal(value, &partition); err != nil || partition != "model" && partition != "none" {
			return mismatch(name+".partition", "model or none")
		}
		o.SortGlobal = partition == "none"
	default:
		if member == "percentile" {
			return invalid("provider." + name + ".percentile is not supported: a sort object takes by and partition; percentile cutoffs belong to preferred_min_throughput and preferred_max_latency, which compare the rolling five-minute p50/p75/p90/p99 samples.")
		}
		return unsupported(name + "." + member)
	}
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
		return 0, mismatch(name, "a number")
	}
	return v, nil
}

// readMinThroughput reads tokens a second as OpenRouter does: a number for the moving average, or an object of
// percentile cutoffs for the rolling five-minute window.
func readMinThroughput(o *routing.Override, name string, value []byte) error {
	if isObject(value) {
		return readCutoffs(name, value, 1e9, &o.MinThroughputPercentiles)
	}
	v, err := jsonNumber(name, value, 1e9)
	o.MinThroughput = v
	return err
}

// readMaxLatency reads seconds, as OpenRouter does, in either form.
func readMaxLatency(o *routing.Override, name string, value []byte) error {
	if isObject(value) {
		return readCutoffs(name, value, 1e6, &o.MaxLatencyPercentiles)
	}
	seconds, err := jsonNumber(name, value, 1e6)
	o.MaxLatency = time.Duration(seconds * float64(time.Second))
	return err
}

func isObject(value []byte) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// percentileIndex names the percentiles an object cutoff can set, as OpenRouter documents them.
var percentileIndex = map[string]int{"p50": routing.P50, "p75": routing.P75, "p90": routing.P90, "p99": routing.P99}

// readCutoffs reads one object of percentile cutoffs. A cutoff of 0 is a threshold, so an object with no members is
// refused rather than read as none: OpenRouter names all four percentiles in one object, and every one given must hold.
func readCutoffs(name string, value []byte, hi float64, c *routing.Cutoffs) error {
	var members map[string]jsonx.Raw
	if err := jsonx.Unmarshal(value, &members); err != nil {
		return mismatch(name, "a number or an object of p50, p75, p90 and p99")
	}
	for _, member := range slices.Sorted(maps.Keys(members)) {
		v := bytes.TrimSpace(members[member])
		if string(v) == "null" {
			continue
		}
		i, known := percentileIndex[member]
		if !known {
			return mismatch(name+"."+member, "one of p50, p75, p90 or p99")
		}
		cutoff, err := jsonNumber(name+"."+member, v, hi)
		if err != nil {
			return err
		}
		c.Values[i], c.Set = cutoff, c.Set|1<<i
	}
	if c.Set == 0 {
		return mismatch(name, "an object with at least one of p50, p75, p90 or p99")
	}
	return nil
}

// maxPriceItems maps OpenRouter caps to Axiom's billing items. Prompt and completion are per million tokens; request
// is one generation, charged whatever its size. A per-image cap stays refused: treating image tokens as images would
// promise the wrong cap.
var maxPriceItems = map[string]catalog.Item{
	"prompt": catalog.ItemInput, "completion": catalog.ItemOutput, "request": catalog.ItemRequest,
}

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
			if member == "image" {
				return invalid("provider." + name + ".image is not supported: the price version has per-piece image items, but OpenRouter's single image cap does not say whether it covers input images, generated images or token-priced images, and no endpoint reports image pieces; the item to compare is undecided.")
			}
			return unsupported(name + "." + member)
		}
		price, ok := perToken(v)
		message := "a number of credits per million tokens with at most 9 decimals, from 0 to 1000000000"
		if item == catalog.ItemRequest {
			price, ok = perRequest(v)
			message = "a number of credits per request with at most 15 decimals, from 0 to 1000"
		}
		if !ok {
			return mismatch(name+"."+member, message)
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
	units, ok := readPrice(number, 9)
	if !ok {
		return platform.UnitPrice{}, false
	}
	return units.price()
}

// perRequest turns a JSON number of credits per request into the price of one request: no million-token scaling, so a
// number is multiplied by 10¹⁵ units, and its 15 decimals are all the unit price has.
func perRequest(number []byte) (platform.UnitPrice, bool) {
	units, ok := readPrice(number, 15)
	if !ok {
		return platform.UnitPrice{}, false
	}
	return units.price()
}

// priceUnits is a number within the bounds of a unit price: its significant decimal digits and the power of ten they
// are multiplied by. Only readPrice makes one, so the big integer in price is reached only by a number whose digits
// and exponent already fit, and a number beyond them costs no more than the string work of the check.
type priceUnits struct {
	digits string
	shift  int
}

// readPrice reads a JSON number as the price units it asks for, or reports that it is not one. It is string work only:
// a huge exponent or a long run of digits is refused here, before any big-integer arithmetic.
func readPrice(number []byte, extra int) (priceUnits, bool) {
	m := priceNumber.FindSubmatch(number)
	if m == nil {
		return priceUnits{}, false
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
		return priceUnits{}, true // the zero value is the price of nothing, whatever the exponent says
	}
	shift := scale + extra
	if len(digits) > 19 || shift < 0 || shift+len(digits) > 19 {
		return priceUnits{}, false
	}
	return priceUnits{digits: digits, shift: shift}, true
}

// price is the unit price u stands for.
func (u priceUnits) price() (platform.UnitPrice, bool) {
	if u.digits == "" {
		return platform.UnitPrice{}, true // the zero value is the price of nothing
	}
	units, _ := new(big.Int).SetString(u.digits, 10)
	units.Mul(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(u.shift)), nil))
	whole, frac := new(big.Int).QuoRem(units, big.NewInt(1e15), new(big.Int))
	fraction := frac.String()
	price, err := platform.ParseUnitPrice(whole.String() + "." + strings.Repeat("0", 15-len(fraction)) + fraction)
	return price, err == nil
}
