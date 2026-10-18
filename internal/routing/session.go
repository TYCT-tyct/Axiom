package routing

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/jsontext" //nolint:depguard // jsonx has no token-by-token reader; the prefix is cut mid-value, which only a token reader handles
	"sync"
	"time"
	"unicode"

	"axiom/internal/protocol"
)

// SessionKey identifies a conversation for stickiness. It is a hash of the account and the conversation's identity, so
// two accounts never share a key whatever session value their clients send.
type SessionKey [sha256.Size]byte

// conversationHead is how much of the conversation's start, in bytes, identifies it when the client names no session.
const conversationHead = 1024

// SessionKey is worked out before admission from the account and the start of the body, reading no further than
// Config.SessionPrefix bytes. The client's own session value is used when there is one: session_id or
// prompt_cache_key, or the user_id of Anthropic's metadata, which Claude Code fills with its session. Without one the
// key comes from the account, the model, and the first bytes of the conversation (docs/domains/routing.md「会话粘性」).
func (r *Router) SessionKey(acct AccountID, req *protocol.Request, body []byte) SessionKey {
	if len(body) > r.cfg.SessionPrefix {
		body = body[:r.cfg.SessionPrefix]
	}
	given, head := scanSession(body)
	h := sha256.New()
	h.Write(acct[:])
	if given != "" {
		h.Write([]byte{1})
		h.Write([]byte(given))
	} else {
		h.Write([]byte{2})
		h.Write([]byte(req.Model))
		h.Write([]byte{0})
		h.Write(head)
	}
	var key SessionKey
	h.Sum(key[:0])
	return key
}

// sessionScan reads the top-level members of a possibly cut-off body.
type sessionScan struct {
	dec      *jsontext.Decoder
	body     []byte
	explicit string // session_id or prompt_cache_key
	user     string // Anthropic's metadata.user_id
	head     []byte // the start of the conversation
}

// scanSession returns the client's session value if it gives one, and the first bytes of the conversation (messages,
// input or contents). The body is cut at the prefix limit, so reading ends in an error at its end, the normal way out.
func scanSession(body []byte) (given string, head []byte) {
	s := sessionScan{dec: jsontext.NewDecoder(bytes.NewBuffer(body)), body: body}
	if tok, err := s.dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return "", nil
	}
	for s.dec.PeekKind() == '"' && s.member() {
	}
	if s.explicit != "" {
		return s.explicit, s.head
	}
	return s.user, s.head
}

// member reads one member and reports whether to read on: not after the session value is found, which wins over the
// rest, or when the body ends.
func (s *sessionScan) member() bool {
	name, err := s.dec.ReadToken()
	if err != nil {
		return false
	}
	switch name.String() {
	case "session_id", "prompt_cache_key":
		v, ok := readString(s.dec)
		s.explicit = v
		return ok && v == ""
	case "metadata":
		s.user, _ = metadataUser(s.dec)
		return true
	case "messages", "input", "contents":
		if s.head == nil {
			s.head = firstItem(s.body[s.dec.InputOffset():])
		}
	}
	return s.dec.SkipValue() == nil
}

// firstItem is the first element of the conversation that value starts: the first message of an array, or the whole value
// when it is a string. It is what stays the same as the conversation grows, and it is cut to conversationHead bytes, or
// to the end of the prefix when the item runs past it. value begins at the member's colon.
func firstItem(value []byte) []byte {
	value = bytes.TrimLeftFunc(bytes.TrimPrefix(bytes.TrimLeftFunc(value, unicode.IsSpace), []byte(":")), unicode.IsSpace)
	dec := jsontext.NewDecoder(bytes.NewBuffer(value))
	tok, err := dec.ReadToken()
	if err != nil { // a string cut off by the prefix has no end yet: its start is the item
		return bounded(value)
	}
	from := int64(0)
	if tok.Kind() == '[' {
		from = dec.InputOffset()
		if dec.PeekKind() == ']' {
			return []byte{}
		}
		if dec.SkipValue() != nil { // cut off by the prefix: the item is what is left
			return bounded(value[from:])
		}
	}
	return bounded(value[from:dec.InputOffset()])
}

// bounded trims the item and cuts it to conversationHead bytes.
func bounded(item []byte) []byte {
	item = bytes.TrimSpace(item)
	return item[:min(len(item), conversationHead)]
}

func readString(dec *jsontext.Decoder) (string, bool) {
	tok, err := dec.ReadToken()
	if err != nil || tok.Kind() != '"' {
		return "", false
	}
	return tok.String(), true
}

// metadataUser reads the metadata object for user_id, skipping what else is in it.
func metadataUser(dec *jsontext.Decoder) (user string, ok bool) {
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return "", false
	}
	for dec.PeekKind() == '"' {
		name, err := dec.ReadToken()
		if err != nil {
			return user, ok
		}
		if name.String() == "user_id" {
			user, ok = readString(dec)
		} else if dec.SkipValue() != nil {
			return user, ok
		}
	}
	_, err := dec.ReadToken() // the object's end
	return user, ok && err == nil
}

const stickyShards = 16

// sticky remembers which endpoint a session last used. Losing an entry only makes the next request choose again; it
// never affects correctness. Each shard holds at most max entries, so memory is bounded.
type sticky struct {
	shards [stickyShards]stickyShard
	max    int
}

type stickyShard struct {
	mu sync.Mutex
	m  map[SessionKey]stuck
}

type stuck struct {
	endpoint EndpointID
	until    time.Time
}

func newSticky(max int) *sticky {
	s := &sticky{max: max/stickyShards + 1}
	for i := range s.shards {
		s.shards[i].m = map[SessionKey]stuck{}
	}
	return s
}

func (s *sticky) shard(k SessionKey) *stickyShard { return &s.shards[k[0]%stickyShards] }

func (s *sticky) get(k SessionKey, now time.Time) (EndpointID, bool) {
	sh := s.shard(k)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok := sh.m[k]
	if ok && !now.Before(e.until) {
		delete(sh.m, k)
		return EndpointID{}, false
	}
	return e.endpoint, ok
}

func (s *sticky) put(k SessionKey, ep EndpointID, until, now time.Time) {
	sh := s.shard(k)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if _, ok := sh.m[k]; !ok && len(sh.m) >= s.max {
		for key, e := range sh.m { // drop what has expired, else one arbitrary entry
			if !now.Before(e.until) {
				delete(sh.m, key)
			}
		}
		for key := range sh.m {
			if len(sh.m) < s.max {
				break
			}
			delete(sh.m, key)
		}
	}
	sh.m[k] = stuck{ep, until}
}

// Stuck is the endpoint the session last used, if its entry has not expired: the gateway reads it before admission and
// passes it as Input.Sticky.
func (r *Router) Stuck(k SessionKey) (EndpointID, bool) { return r.sticky.get(k, r.cfg.Now()) }

// Stick records that the session was served by the endpoint, for the preset's sticky time (the router's default when
// the preset sets none). A fallback that succeeds moves the session to the endpoint that served it.
func (r *Router) Stick(k SessionKey, ep EndpointID, ttl time.Duration) {
	now := r.cfg.Now()
	if ttl <= 0 {
		ttl = r.cfg.StickyTTL
	}
	r.sticky.put(k, ep, now.Add(ttl), now)
}

// Forget drops the session's entry: the client says the context was compacted, so the cache it stuck for is gone.
func (r *Router) Forget(k SessionKey) {
	sh := r.sticky.shard(k)
	sh.mu.Lock()
	delete(sh.m, k)
	sh.mu.Unlock()
}
