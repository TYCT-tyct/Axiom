package accounts

import (
	"context"
	"net/netip"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/clientip"
	"axiom/internal/platform/pg"
)

// SessionDevice is one signed-in console session as the device list shows it: when it was made and last used, the
// network it was signed in from, and a short client description. The token and its CSRF secret are never part of it.
type SessionDevice struct {
	ID         SessionID
	Current    bool
	CreatedAt  time.Time
	LastSeenAt time.Time
	IPGroup    string
	Client     string
}

// Sessions is p's live console sessions, newest first, at most 100: the current one is marked. Pending sign-ins,
// sessions of another surface and sessions invalidated by a factor change are not devices and are left out, with the
// same validity cuts ValidSession applies.
func (s *Service) Sessions(ctx context.Context, p Principal) ([]SessionDevice, error) {
	if p.user == (UserID{}) || p.surface != Console || p.Pending() {
		return nil, ErrNoSession
	}
	maxAge, idle := s.snapshot.Load().sites[p.site].sessions[Console].seconds()
	var rows []ListSessionsRow
	if err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		var err error
		rows, err = s.queries.ListSessions(ctx, tx, ListSessionsParams{
			UserID: uuid.UUID(p.user), SiteID: uuid.UUID(p.site), MaxAgeSeconds: maxAge, IdleSeconds: idle,
		})
		return err
	}); err != nil {
		return nil, err
	}
	devices := make([]SessionDevice, 0, len(rows))
	for _, row := range rows {
		id := SessionID(row.ID)
		devices = append(devices, SessionDevice{
			ID: id, Current: id == p.session, CreatedAt: row.CreatedAt.Time, LastSeenAt: row.LastSeenAt.Time,
			IPGroup: ipGroup(row.Ip), Client: clientSummary(row.UserAgent),
		})
	}
	return devices, nil
}

// RevokeSession ends one other console session of p's user, as ErrNotFound when the ID names no such live session:
// another user's, another surface's, the asking session itself and one already ended are alike.
func (s *Service) RevokeSession(ctx context.Context, p Principal, id SessionID) error {
	if p.user == (UserID{}) || p.surface != Console || p.Pending() {
		return ErrNoSession
	}
	maxAge, idle := s.snapshot.Load().sites[p.site].sessions[Console].seconds()
	return s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		revoked, err := s.queries.RevokeDeviceSession(ctx, tx, RevokeDeviceSessionParams{
			ID: uuid.UUID(id), SiteID: uuid.UUID(p.site), UserID: uuid.UUID(p.user), CurrentID: uuid.UUID(p.session),
			FactorVersion: p.factorVersion, MaxAgeSeconds: maxAge, IdleSeconds: idle,
		})
		if err != nil {
			return err
		}
		if revoked != 1 {
			return ErrNotFound
		}
		return audit.Record(ctx, tx, audit.Event{Actor: audit.User(uuid.UUID(p.user)), Action: "session.revoke", Target: audit.Target{
			Module: "accounts", Kind: "session", ID: id.TypeID(), Site: uuid.UUID(p.site), Account: uuid.UUID(p.account),
		}})
	})
}

// RevokeOtherSessions ends every other live console session of p's user, pending sign-ins included, and returns how
// many ended. The current session stays, so the caller keeps the page they are on.
func (s *Service) RevokeOtherSessions(ctx context.Context, p Principal) (int, error) {
	if p.user == (UserID{}) || p.surface != Console || p.Pending() {
		return 0, ErrNoSession
	}
	maxAge, idle := s.snapshot.Load().sites[p.site].sessions[Console].seconds()
	var revoked int64
	err := s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		// Lock the user first, as VerifyMFA does, so a second-factor sign-in that commits after this statement began
		// cannot insert a new session the UPDATE's snapshot misses.
		if _, err := s.lockUser(ctx, tx, uuid.UUID(p.user), p.factorVersion); err != nil {
			return err
		}
		var err error
		revoked, err = s.queries.RevokeOtherSessions(ctx, tx, RevokeOtherSessionsParams{
			SiteID: uuid.UUID(p.site), UserID: uuid.UUID(p.user), CurrentID: uuid.UUID(p.session),
			FactorVersion: p.factorVersion, MaxAgeSeconds: maxAge, IdleSeconds: idle,
		})
		if err != nil || revoked == 0 {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.User(uuid.UUID(p.user)), Action: "session.revoke_others",
			Target: audit.Target{Module: "accounts", Kind: "user", ID: p.user.TypeID(), Site: uuid.UUID(p.site), Account: uuid.UUID(p.account)},
			Detail: map[string]any{"revoked": revoked},
		})
	})
	return int(revoked), err
}

// ipGroup is the address as the login limits count it, so the list shows the same group they do (clientip.Group:
// an IPv4 address as it is, an IPv6 allocation as its /64). An unreadable or absent address is left out rather than
// shown raw.
func ipGroup(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return ""
	}
	return clientip.Group(ip).String()
}

// clientSummary names the browser and platform a stored User-Agent describes, so the list shows a short, stable
// description instead of returning the header the client chose. Order matters: Edge and Opera also say Chrome, Chrome
// says Safari, Android says Linux, and an iPhone says Mac OS X.
func clientSummary(ua string) string {
	browser, platform := browserName(ua), platformName(ua)
	client := browser
	if client == "" {
		client = "Unknown client"
	}
	if platform != "" {
		client += " on " + platform
	}
	return client
}

// browserName is the first known browser token in the User-Agent, or empty.
func browserName(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "EdgA/"), strings.Contains(ua, "EdgiOS/"):
		return "Edge"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera"), strings.Contains(ua, "OPiOS/"):
		return "Opera"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS/"):
		return "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS"):
		return "Chrome"
	case strings.Contains(ua, "Safari/"):
		return "Safari"
	case strings.Contains(ua, "curl/"):
		return "curl"
	case strings.Contains(ua, "python-requests/"):
		return "Python requests"
	}
	return ""
}

// platformName is the first known platform token in the User-Agent, or empty.
func platformName(ua string) string {
	switch {
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "iOS"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "CrOS"):
		return "ChromeOS"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return "macOS"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	}
	return ""
}
