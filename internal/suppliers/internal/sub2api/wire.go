package sub2api

import (
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// envelope is sub2api's {code, message, data} wrapper of a successful answer.
type envelope[T any] struct {
	Code number `json:"code"`
	Data T      `json:"data"`
}

// open decodes a successful answer's data. A 2xx whose code is not 0 is not one.
func open[T any](resp adapter.Response) (T, error) {
	var e envelope[T]
	if err := decode(resp.Body, &e); err != nil {
		return e.Data, err
	}
	if e.Code != "0" {
		return e.Data, fmt.Errorf("%w: a success status with a non-zero code", adapter.ErrUnrecognised)
	}
	return e.Data, nil
}

// errorCode is the machine-readable code of an error answer: the reason when there is one, else the code when it is
// a string. The three forms seen are {"code":"X"}, {"code":401,"reason":"X"} and {"error":..}, which has none.
func errorCode(body []byte) string {
	var e struct {
		Code   any    `json:"code"`
		Reason string `json:"reason"`
	}
	if jsonx.Unmarshal(body, &e) != nil {
		return ""
	}
	if e.Reason != "" {
		return e.Reason
	}
	code, _ := e.Code.(string)
	return code
}

// retryAfter reads a Retry-After in seconds; a date, a negative number or nothing is zero.
func retryAfter(h http.Header) time.Duration {
	n, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// page is sub2api's {items, total, page, page_size, pages} list.
type page[T any] struct {
	Items []T `json:"items"`
	Pages int `json:"pages"`
}

// pathID builds a path segment from an id the supplier gave, refusing anything but digits so that an id cannot add
// path or query.
func pathID(id string) (string, error) {
	if id == "" || len(id) > 18 || strings.Trim(id, "0123456789") != "" {
		return "", fmt.Errorf("%w: an id that is not a number", adapter.ErrUnrecognised)
	}
	return id, nil
}
