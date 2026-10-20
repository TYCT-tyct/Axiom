package sub2api

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

// number keeps a JSON number as written. sub2api's amounts are Go floats printed shortest-form (1e-7 for a tenth of a
// micro-dollar), so a float64 would round them and the text may use an exponent; decimal turns the text into the
// plain decimal an Amount is.
type number string

// UnmarshalJSON keeps the raw text; null is an absent number.
func (n *number) UnmarshalJSON(b []byte) error {
	if string(b) != "null" {
		*n = number(b)
	}
	return nil
}

func (n number) present() bool { return n != "" }

// amountPlaces is as many decimals as sub2api states (a charge has ten); a number with more is refused rather than
// rounded, because a rounded cost shows up as a difference at reconciliation.
const amountPlaces = 12

func (n number) amount() (adapter.Amount, error) {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return "", fmt.Errorf("%w: an amount that is not a number", adapter.ErrUnrecognised)
	}
	text := r.FloatString(amountPlaces)
	if back, _ := new(big.Rat).SetString(text); back.Cmp(r) != 0 {
		return "", fmt.Errorf("%w: an amount with more than %d decimals", adapter.ErrUnrecognised, amountPlaces)
	}
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return adapter.Amount(text), nil
}

func (n number) int64() (int64, error) {
	v, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: a count that is not an integer", adapter.ErrUnrecognised)
	}
	return v, nil
}

// decode reads a response body. A failure says only that the form is unknown: the decoder's message may quote a
// fragment of the body, which can hold a credential.
func decode(body []byte, v any) error {
	if err := jsonx.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: the body does not have the expected shape", adapter.ErrUnrecognised)
	}
	return nil
}

// envelope is sub2api's {code, message, data} wrapper of a successful answer. Data is a pointer, so that an answer
// without it is told from one with an empty value.
type envelope[T any] struct {
	Code number `json:"code"`
	Data *T     `json:"data"`
}

// open decodes a successful answer's data. A 2xx whose code is not 0, or that has no data, is not one: an answer in
// another form than the one known must stop the task, not read as "nothing there".
func open[T any](resp adapter.Response) (T, error) {
	var e envelope[T]
	if err := decode(resp.Body, &e); err != nil {
		var zero T
		return zero, err
	}
	if e.Code != "0" || e.Data == nil {
		var zero T
		return zero, fmt.Errorf("%w: a success status without code 0 and data", adapter.ErrUnrecognised)
	}
	return *e.Data, nil
}

// knownCodes are the machine-readable codes sub2api's answers are known to carry. Only these ever leave errorCode: an
// answer may echo a credential in any member, so a code that is not on the list is not passed on, not even in an error.
var knownCodes = map[string]bool{
	"TOKEN_EXPIRED": true, "INVALID_TOKEN": true, "UNAUTHORIZED": true, "TOKEN_REVOKED": true, "USER_INACTIVE": true,
	"SESSION_BINDING_MISMATCH": true, "INVALID_CREDENTIALS": true, "REFRESH_TOKEN_INVALID": true,
	"INSUFFICIENT_BALANCE": true, "INVALID_API_KEY": true, "API_KEY_QUOTA_EXHAUSTED": true, "USAGE_LIMIT_EXCEEDED": true,
	"rate_limit_exceeded": true,
}

// errorCode is the known code of an error answer: the reason when there is one, else the code when it is a string. The
// forms seen are {"code":"X"}, {"code":401,"reason":"X"} and {"error":..}, which has none. A code that is not on
// knownCodes is "".
func errorCode(body []byte) string {
	var e struct {
		Code   any    `json:"code"`
		Reason string `json:"reason"`
	}
	if jsonx.Unmarshal(body, &e) != nil {
		return ""
	}
	code := e.Reason
	if code == "" {
		code, _ = e.Code.(string)
	}
	if !knownCodes[code] {
		return ""
	}
	return code
}

// codeName is a code for an error message: the known code, or a fixed word.
func codeName(code string) string {
	if code == "" {
		return "no known code"
	}
	return code
}

// page is sub2api's {items, total, page, page_size, pages} list.
type page[T any] struct {
	Items []T  `json:"items"`
	Pages *int `json:"pages"`
}

// checked returns the page's items, or ErrUnrecognised if the list or its page count is missing: a page without them is
// not an empty page.
func (p page[T]) checked() ([]T, int, error) {
	if p.Items == nil || p.Pages == nil {
		return nil, 0, fmt.Errorf("%w: a page without its items or page count", adapter.ErrUnrecognised)
	}
	return p.Items, *p.Pages, nil
}

// pathID builds a path segment from an id the supplier gave, refusing anything but digits so that an id cannot add
// path or query.
func pathID(id string) (string, error) {
	if id == "" || len(id) > 18 || strings.Trim(id, "0123456789") != "" {
		return "", fmt.Errorf("%w: an id that is not a number", adapter.ErrUnrecognised)
	}
	return id, nil
}
