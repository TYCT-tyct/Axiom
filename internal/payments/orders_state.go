package payments

import "slices"

// State is where an order is (docs/domains/payments.md「订单」).
type State string

// The order states. A payment is confirmed by the callback, a query or a person (README.md「实现拆分」E1-2c).
const (
	Pending           State = "pending"
	Paid              State = "paid"
	Expired           State = "expired"
	Cancelled         State = "cancelled"
	Failed            State = "failed"
	Review            State = "review"
	PartiallyRefunded State = "partially_refunded"
	Refunded          State = "refunded"
	ChargedBack       State = "charged_back"
)

// states are all of them, in the order the design lists them, so a set of states is always the same slice.
var states = []State{Pending, Paid, Expired, Cancelled, Failed, Review, PartiallyRefunded, Refunded, ChargedBack}

// transitions is the one table of what an order may become (docs/domains/payments.md「订单」): an order's state moves
// along these edges and no others, so a lost or repeated event cannot walk it backwards.
var transitions = map[State][]State{
	Pending:           {Paid, Review, Expired, Cancelled, Failed},
	Expired:           {Paid, Review},
	Cancelled:         {Paid, Review},
	Failed:            {Paid, Review},
	Review:            {Paid, Refunded},
	Paid:              {PartiallyRefunded, Refunded, ChargedBack},
	PartiallyRefunded: {PartiallyRefunded, Refunded, ChargedBack},
	Refunded:          {ChargedBack},
}

// canMoveTo reports whether an order may go from s to next.
func (s State) canMoveTo(next State) bool { return slices.Contains(transitions[s], next) }

// predecessors is every state an order may come to next from: the condition a change of an order's state carries into
// the database, so the row changes only along an edge of the table.
func predecessors(next State) []string {
	var from []string
	for _, s := range states {
		if s.canMoveTo(next) {
			from = append(from, string(s))
		}
	}
	return from
}
