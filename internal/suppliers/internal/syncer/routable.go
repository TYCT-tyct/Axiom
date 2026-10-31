package syncer

// Reason says why an endpoint is kept out of routing, as it is written with the endpoint (internal/suppliers/README.md
// 「同步」); empty when nothing keeps it out.
type Reason string

// The reasons.
const (
	Routable    Reason = ""
	Unmapped    Reason = "the model is not mapped"
	NotListed   Reason = "the model is not listed in the catalog"
	CostUnknown Reason = "the cost is not known"
	// Gone is an endpoint whose model the listing no longer offers: the row is kept for when it comes back and is taken
	// out of routing meanwhile.
	Gone Reason = "the listing no longer offers the model"
	// NoFormat is a name the listing still has in a group nothing can be called from — one sold by subscription, or one
	// whose price changes by the hour: its formats are none, so it is not a model of the supplier at all.
	NoFormat Reason = "no call format is served here"
	// DirectFormatOff is a name the listing serves only in a format this deployment has not enabled writing yet: it
	// waits for the compatibility gate rather than disappearing from the listing.
	DirectFormatOff Reason = "direct formats are not enabled on this deployment"
)
