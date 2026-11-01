package platform

import "uuid"

// RequestID identifies one call to the API, from the moment it is accepted. It is a version 7 UUID made once, so it
// sorts by time and is the same in every table that records the call.
type RequestID uuid.UUID

func (id RequestID) String() string { return uuid.UUID(id).String() }

// MaxAttempts is the most route entries and credentials one request tries, including attempts whose outcome is unknown.
const MaxAttempts = 3

// MaxServerToolSteps is the most steps of a model-side loop one request makes: OpenRouter's ceiling of max_tool_calls.
// Each step is one more sampling, and so one more attempt.
const MaxServerToolSteps = 30

// MaxAttemptSeq is the highest attempt number. Every supplier request is an attempt: the routing tries, and each further
// sampling of a model-side loop, which a request runs at most once (a loop's searches end its fallbacks), so the two add
// up. The tables' CHECKs (suppliers 00024, observability 00016) hold the same 33.
const MaxAttemptSeq = MaxAttempts + MaxServerToolSteps

// AttemptSeq numbers an attempt within its request, from 1, in the order the attempts begin.
type AttemptSeq uint8

// Valid reports whether s is a number an attempt can have.
func (s AttemptSeq) Valid() bool { return s >= 1 && s <= MaxAttemptSeq }
