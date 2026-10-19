package routing

// Scope is what a failed attempt says is at fault. It lists the scopes of suppliers' Failure; gateway maps one to the
// other by name.
type Scope uint8

// The scopes.
const (
	ScopeRequest    Scope = iota // the request itself: retrying it elsewhere fails the same way
	ScopeEgress                  // the route to the supplier
	ScopeEndpoint                // the one group and model
	ScopeCredential              // the key
	ScopeGroup                   // everything in the group
	ScopeSupplier                // everything at the supplier
)

// Failure is the part of a failed attempt that decides what comes next.
type Failure struct{ Scope Scope }

// Action is what to do after a failed attempt that nothing has been forwarded for.
type Action uint8

// The actions.
const (
	NextEntry       Action = iota // the next entry of the plan
	OtherCredential               // the same endpoint with another credential or egress
	ReturnToClient                // the same on any endpoint: give the client the error
)

// Decide maps a failure to what to do next (docs/domains/routing.md「失败怎么办」). The table's rows are told apart by
// the scope the adapter gives: a malformed request is the request's own; a limit, an overload, a timeout, a context that
// is too long, a refusal by moderation or a missing model is the endpoint's, the group's or the supplier's, so the next
// entry may do; a key that is refused or used up is the credential's, and another credential of the endpoint may do; an
// unreachable route is the egress's: changing only the credential cannot fix it, and every endpoint has one egress
// (direct), so the next entry may do. Once the first byte of the
// reply has gone to the client nothing is retried, and Decide is not asked.
func Decide(f Failure) Action {
	switch f.Scope {
	case ScopeRequest:
		return ReturnToClient
	case ScopeCredential:
		return OtherCredential
	case ScopeEgress, ScopeEndpoint, ScopeGroup, ScopeSupplier:
		return NextEntry
	}
	return NextEntry
}
