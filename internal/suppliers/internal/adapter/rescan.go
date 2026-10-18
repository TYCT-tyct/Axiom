package adapter

import "time"

// Rescanner is a Manager that can be told to read the charge records again from a given time. The engine does not ask
// for it now: it was for a removal that read the records a last time (a removal is one transaction of the admin now).
// The state returned begins a pass over
// [since, now) and keeps the adapter's other position as it is. A Manager without it is read on with Charges as before.
type Rescanner interface {
	Rescan(since time.Time) ScanState
}
