package adapter

import "time"

// Rescanner is a Manager that can be told to read the charge records again from a given time. A removal uses it after
// the grace period to take the records that appeared late, which the passes the adapter itself schedules might have
// left to a later late pass that will not come. The state returned begins a pass over [since, now) and keeps the
// adapter's other position as it is. A Manager without it is read on with Charges as before.
type Rescanner interface {
	Rescan(since time.Time) ScanState
}
