package platform

import "uuid"

// RequestID identifies one call to the API, from the moment it is accepted. It is a version 7 UUID made once, so it
// sorts by time and is the same in every table that records the call.
type RequestID uuid.UUID

func (id RequestID) String() string { return uuid.UUID(id).String() }

// MaxAttempts is the most attempts one request makes, including those whose outcome is unknown.
const MaxAttempts = 3

// AttemptSeq numbers an attempt within its request, from 1, in the order the attempts begin.
type AttemptSeq uint8

// Valid reports whether s is a number an attempt can have.
func (s AttemptSeq) Valid() bool { return s >= 1 && s <= MaxAttempts }
