package accounts

import (
	"uuid"

	"axiom/internal/platform/typeid"
)

// Each kind of ID is a type of its own, so one cannot be passed where another is meant. None implements
// TextMarshaler: the HTTP boundary converts explicitly, behind the kind's TypeID prefix, and nothing else writes an
// ID another way (README.md「ID」).

var (
	announcementPrefix = typeid.MustPrefix("ann")
	sitePrefix         = typeid.MustPrefix("site")
	tierPrefix         = typeid.MustPrefix("tier")
	accountPrefix      = typeid.MustPrefix("acct")
	workspacePrefix    = typeid.MustPrefix("ws")
	userPrefix         = typeid.MustPrefix("user")
	keyPrefix          = typeid.MustPrefix("key")
	sessionPrefix      = typeid.MustPrefix("sess")
	passkeyPrefix      = typeid.MustPrefix("pk")
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

// UserID is a user's ID.
type UserID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id UserID) TypeID() string { return userPrefix.Encode(uuid.UUID(id)) }

// ParseUserID reads what TypeID writes; anything else, another kind's ID included, is typeid.ErrInvalid.
func ParseUserID(s string) (UserID, error) {
	id, err := userPrefix.Decode(s)
	return UserID(id), err
}

// KeyID is an API key's ID.
type KeyID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id KeyID) TypeID() string { return keyPrefix.Encode(uuid.UUID(id)) }

// ParseKeyID reads what TypeID writes; anything else, another kind's ID included, is typeid.ErrInvalid.
func ParseKeyID(s string) (KeyID, error) {
	id, err := keyPrefix.Decode(s)
	return KeyID(id), err
}

// SessionID is a session's ID.
type SessionID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id SessionID) TypeID() string { return sessionPrefix.Encode(uuid.UUID(id)) }

// ParseSessionID reads what TypeID writes; anything else, another kind's ID included, is typeid.ErrInvalid.
func ParseSessionID(s string) (SessionID, error) {
	id, err := sessionPrefix.Decode(s)
	return SessionID(id), err
}

// PasskeyID is a passkey's ID.
type PasskeyID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id PasskeyID) TypeID() string { return passkeyPrefix.Encode(uuid.UUID(id)) }

// ParsePasskeyID reads only passkey TypeIDs.
func ParsePasskeyID(s string) (PasskeyID, error) {
	id, err := passkeyPrefix.Decode(s)
	return PasskeyID(id), err
}

// AnnouncementID is a site announcement's ID.
type AnnouncementID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id AnnouncementID) TypeID() string { return announcementPrefix.Encode(uuid.UUID(id)) }

// ParseAnnouncementID reads only announcement TypeIDs.
func ParseAnnouncementID(s string) (AnnouncementID, error) {
	id, err := announcementPrefix.Decode(s)
	return AnnouncementID(id), err
}
