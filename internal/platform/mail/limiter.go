package mail

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
)

const (
	// mailLabel derives the recipient key from the master key under a label of its own, as the login keys do: the API
	// keys' hash key rotates with two versions at once, and that must not split a recipient between nodes
	// (internal/platform/kv/README.md「键」).
	mailLabel = "axiom kv mail v1"
	// mailKeyLen is the length of the recipient key, and of the master key it comes from.
	mailKeyLen = 32
)

// admitScript admits one recipient for one interval: the key holds the moment the recipient may be messaged again, and
// a key whose moment has passed is written over even while the server has it a little longer. The write and the check
// are one script, so two nodes admitting the same recipient at once cannot both win
// (internal/platform/kv/README.md「每种用途」).
const admitScript = `
local deadline = tonumber(redis.call('GET', KEYS[1]))
local now = tonumber(ARGV[1])
if deadline and deadline > now then return {0} end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return {1}`

// admitRecipient runs that script; the answer is a list of one integer.
var admitRecipient = kv.NewScript(admitScript)

// limiter allows one message per recipient per interval. It is the fallback while the shared store is away, and the
// whole throttle in single-core mode: each node then keeps its own flow from filling an inbox, at the cost of at most
// one message per node per interval during an outage.
type limiter struct {
	mu       sync.Mutex
	last     map[string]time.Time // the recipient's last message; keys are lowercased addresses
	now      func() time.Time
	interval time.Duration
}

// newLimiter starts with nobody messaged.
func newLimiter() *limiter {
	return &limiter{last: make(map[string]time.Time), now: time.Now, interval: rateInterval}
}

// allow reports whether the recipient may be messaged now, and records the attempt when it may. Every call also
// drops the recipients of earlier intervals, so the map holds only who was messaged recently rather than everyone
// ever addressed, and an attacker choosing recipients cannot grow it without bound.
func (l *limiter) allow(recipient string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, at := range l.last {
		if now.Sub(at) >= l.interval {
			delete(l.last, key)
		}
	}
	// Mail is case-insensitive in practice: one inbox must not get a message per spelling of its address.
	key := strings.ToLower(recipient)
	if _, messaged := l.last[key]; messaged {
		return false
	}
	l.last[key] = now
	return true
}

// throttle admits a recipient in the shared store while it answers, and in this node's limiter while it does not.
// Admission is spent before the send, so a failed send keeps the interval: the retry is snoozed rather than sent at
// once, which is what the queue did when the count lived here alone.
type throttle struct {
	local *limiter
	cache *kv.Client       // nil when the node has no shared store
	key   [mailKeyLen]byte // the recipient key's HMAC key
}

// newThrottle starts on this node's limiter alone; Register adds the shared store when there is one.
func newThrottle() *throttle { return &throttle{local: newLimiter()} }

// allow reports whether the recipient may be messaged now, and records the admission when it may.
func (t *throttle) allow(ctx context.Context, recipient string) bool {
	if t.cache != nil && t.cache.Enabled() {
		if allowed, shared := t.admit(ctx, recipient); shared {
			return allowed
		}
	}
	return t.local.allow(recipient)
}

// admit asks the shared store. A failed access falls back to this node's limiter; an answer of "no" is the shared
// store's decision and is not overruled locally.
func (t *throttle) admit(ctx context.Context, recipient string) (allowed, shared bool) {
	now := t.cache.Now()
	interval := t.local.interval
	values, err := t.cache.Do(ctx, kv.Mail, admitRecipient.Cmd([]string{t.keyFor(recipient)},
		strconv.FormatInt(now.UnixMilli(), 10),
		strconv.FormatInt(now.Add(interval).UnixMilli(), 10),
		strconv.FormatInt(millis(interval), 10)))
	if err != nil {
		return false, false
	}
	answers, err := values[0].Ints()
	if err != nil || len(answers) != 1 {
		return false, false // the script answers one integer; anything else is this node's to decide
	}
	return answers[0] == 1, true
}

// keyFor is the recipient's key in the shared store. The subject is an HMAC so that a SCAN or a memory dump reads no
// address, as the login keys already do (internal/platform/kv/README.md「键」). One recipient is one key whatever the
// site or the spelling: the inbox is the same one, which is the scope the in-process limiter had.
func (t *throttle) keyFor(recipient string) string {
	mac := hmac.New(sha256.New, t.key[:])
	mac.Write([]byte(strings.ToLower(recipient)))
	return t.cache.Key(kv.MailSpace, uuid.Nil(), hex.EncodeToString(mac.Sum(nil)[:16]))
}

// millis is a duration as the script counts it: whole milliseconds, rounded up, so that the key never expires early.
func millis(d time.Duration) int64 { return max(1, (d + time.Millisecond - 1).Milliseconds()) }

// deriveMailKey is the HMAC key of the recipients in the mail keys: HKDF-SHA256 from the master key's current
// version, under a label of its own.
func deriveMailKey(master []byte) ([mailKeyLen]byte, error) {
	if len(master) != mailKeyLen {
		return [mailKeyLen]byte{}, errors.New("mail: a shared cache needs the master key, which the recipient keys are derived from")
	}
	key, err := hkdf.Key(sha256.New, master, nil, mailLabel, mailKeyLen)
	if err != nil {
		return [mailKeyLen]byte{}, fmt.Errorf("mail: derive the recipient key: %w", err)
	}
	return [mailKeyLen]byte(key), nil
}
