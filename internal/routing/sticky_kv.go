package routing

import (
	"encoding/hex"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
)

// StickyKey isolates a session by site as well as by the account already hashed into SessionKey.
func StickyKey(cache *kv.Client, site uuid.UUID, session SessionKey) string {
	var text [64]byte
	hex.Encode(text[:], session[:])
	return cache.Key(kv.StickySpace, site, string(text[:]))
}

// StickyEndpoint reads routing's binding format. Missing or malformed cache data means choosing afresh.
func StickyEndpoint(value kv.Value) EndpointID {
	if value.Missing() {
		return EndpointID{}
	}
	text, err := value.String()
	if err != nil || len(text) != 32 {
		return EndpointID{}
	}
	var endpoint EndpointID
	if _, err := hex.Decode(endpoint[:], []byte(text)); err != nil {
		return EndpointID{}
	}
	return endpoint
}

// StickyWrite uses the same default as the in-process table and always writes an expiring binding.
func (r *Router) StickyWrite(key string, endpoint EndpointID, ttl time.Duration) kv.Cmd {
	return kv.Set(key, kv.Hex(uuid.UUID(endpoint)), r.stickyTTL(ttl))
}
