package payments

import "strings"

// The sources and kinds of the order events this module writes; the callback and the query add theirs (README.md
// 「实现拆分」E1-2c).
const (
	sourceUser = "user"
	sourceJob  = "job"

	kindCreate   = "create"
	kindCheckout = "checkout"
	kindReject   = "reject"
	kindCancel   = "cancel"
	kindExpire   = "expire"
)

// maxRaw is what an order event's raw text holds, and so how far a gateway's answer is cut.
const maxRaw = 8 << 10

// event is what happened to an order, as an order event records it.
type event struct {
	source  string
	kind    string
	tradeNo string
	amount  *int64
	raw     string
}

// cut is raw as far as the event's column holds.
func cut(raw string) string {
	raw = strings.ReplaceAll(raw, "\x00", "")
	if len(raw) <= maxRaw {
		return strings.ToValidUTF8(raw, "")
	}
	return strings.ToValidUTF8(raw[:maxRaw], "")
}
