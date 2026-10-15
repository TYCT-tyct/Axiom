package accounts

import (
	"uuid"

	"axiom/internal/platform/typeid"
)

// Each kind of ID is a type of its own, so one cannot be passed where another is meant. None implements
// TextMarshaler: the HTTP boundary converts explicitly, behind the kind's TypeID prefix, and nothing else writes an
// ID another way (README.md「ID」).

var (
	sitePrefix      = typeid.MustPrefix("site")
	tierPrefix      = typeid.MustPrefix("tier")
	accountPrefix   = typeid.MustPrefix("acct")
	workspacePrefix = typeid.MustPrefix("ws")
)

// SiteID is a site's ID.
type SiteID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id SiteID) TypeID() string { return sitePrefix.Encode(uuid.UUID(id)) }

// ParseSiteID reads what TypeID writes; anything else, another kind's ID included, is typeid.ErrInvalid.
func ParseSiteID(s string) (SiteID, error) {
	id, err := sitePrefix.Decode(s)
	return SiteID(id), err
}

// TierID is a tier's ID.
type TierID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id TierID) TypeID() string { return tierPrefix.Encode(uuid.UUID(id)) }

// ParseTierID reads what TypeID writes; anything else, another kind's ID included, is typeid.ErrInvalid.
func ParseTierID(s string) (TierID, error) {
	id, err := tierPrefix.Decode(s)
	return TierID(id), err
}

// AccountID is an account's ID.
type AccountID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id AccountID) TypeID() string { return accountPrefix.Encode(uuid.UUID(id)) }

// ParseAccountID reads what TypeID writes; anything else, another kind's ID included, is typeid.ErrInvalid.
func ParseAccountID(s string) (AccountID, error) {
	id, err := accountPrefix.Decode(s)
	return AccountID(id), err
}

// WorkspaceID is a workspace's ID.
type WorkspaceID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id WorkspaceID) TypeID() string { return workspacePrefix.Encode(uuid.UUID(id)) }

// ParseWorkspaceID reads what TypeID writes; anything else, another kind's ID included, is typeid.ErrInvalid.
func ParseWorkspaceID(s string) (WorkspaceID, error) {
	id, err := workspacePrefix.Decode(s)
	return WorkspaceID(id), err
}
