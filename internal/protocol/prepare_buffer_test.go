package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// userIDLen is how many bytes that structure adds around the derived session.
const userIDLen = len(`{"device_id":"","account_uuid":"","session_id":""}`)

// TestPrepareBuffer holds the output buffer to the room README.md「同格式改写」 promises: whatever Prepare adds, with
// derived identifiers as long as Rewrites.IdentityLen says, fits without the body being copied again. The body is small
// so that the allocator's rounding up leaves less room than what is added.
func TestPrepareBuffer(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef"
	body := []byte(`{"model":"m","previous_response_id":"r","input":"next","prompt_cache_key":"k","user":"u","safety_identifier":"s"}`)
	allocs := func(identity string, identityLen int) float64 {
		w := protocol.Rewrites{Identity: func(string) string { return identity }, IdentityLen: identityLen, History: history}
		r := inspected(t, protocol.Responses, body)
		return testing.AllocsPerRun(5, func() {
			if _, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Responses, Model: "up", EncryptedReasoning: catalog.ClaimYes}, w); err != nil {
				t.Fatal(err)
			}
		})
	}
	long := allocs(id, len(id))
	// A one-byte identifier replaces a one-byte original: the body grows only by what is added.
	if short := allocs("k", 0); long != short {
		t.Errorf("%d allocations with long identifiers, %d with short ones", int(long), int(short))
	}
	// Room to spare for the identifiers, which the added members must fit in as well.
	if roomy := allocs(id, 10*len(id)); long != roomy {
		t.Errorf("%d allocations with room for the identifiers, %d with room to spare", int(long), int(roomy))
	}
}

// TestPrepareBufferEscapes holds the room to the JSON escapes of the values put in: a model name and identifiers
// made of bytes that take six bytes each still fit in one allocation, as short ones do.
func TestPrepareBufferEscapes(t *testing.T) {
	nasty := strings.Repeat("\x01\"\\", 40)
	for _, c := range []struct {
		format protocol.Format
		body   string
		wraps  int // the bytes the format's derived value adds around Identity's own
	}{
		{protocol.Messages, `{"model":"m","metadata":{"user_id":"u"}}`, userIDLen},
		{protocol.Responses, `{"model":"m","prompt_cache_key":"k","user":"u","safety_identifier":"s"}`, 0},
	} {
		allocs := func(model, identity string) float64 {
			bound := len(identity)
			if c.format == protocol.Messages {
				encoded, err := jsonx.MarshalCanonical(identity)
				if err != nil {
					t.Fatal(err)
				}
				bound = len(encoded) - 2
			}
			w := protocol.Rewrites{Identity: func(string) string { return identity }, IdentityLen: bound + c.wraps}
			r := inspected(t, c.format, []byte(c.body))
			return testing.AllocsPerRun(5, func() {
				if _, err := protocol.Prepare(&r, []byte(c.body), protocol.Target{Format: c.format, Model: model}, w); err != nil {
					t.Fatal(err)
				}
			})
		}
		if long, short := allocs(nasty, nasty), allocs("m", "u"); long != short {
			t.Errorf("format %d: %d allocations with escaped values, %d without", c.format, int(long), int(short))
		}
	}
}

// TestPrepareBufferDefaultIdentityLen holds the room for derived values to the default bound when IdentityLen is not set:
// a value as long as the bound, all of bytes that need escaping, replacing an original that needs none, fits in one
// allocation; a longer one is refused as the gateway's error. The structure of metadata.user_id counts towards the bound,
// so the Messages case also counts the inner escaping.
func TestPrepareBufferDefaultIdentityLen(t *testing.T) {
	for _, c := range []struct {
		format protocol.Format
		body   string
		wraps  int // the bytes the format's derived value adds around Identity's own
	}{
		{protocol.Messages, `{"model":"m","metadata":{"user_id":"` + strings.Repeat("a", 128) + `"}}`, userIDLen},
		{protocol.Responses, `{"model":"m","prompt_cache_key":"` + strings.Repeat("a", 128) + `","user":"u","safety_identifier":"s"}`, 0},
	} {
		// prepareWith derives an identity of n bytes, which with what the format wraps around it makes up the value the
		// bound applies to.
		prepareWith := func(n int) (float64, error) {
			id := strings.Repeat(`\`, n)
			w := protocol.Rewrites{Identity: func(string) string { return id }}
			r := inspected(t, c.format, []byte(c.body))
			var err error
			allocs := testing.AllocsPerRun(5, func() {
				_, err = protocol.Prepare(&r, []byte(c.body), protocol.Target{Format: c.format, Model: "m"}, w)
			})
			return allocs, err
		}
		n := 128 - c.wraps
		if c.format == protocol.Messages {
			n /= 2
		} // A backslash takes two bytes in the inner object.
		long, err := prepareWith(n)
		if err != nil {
			t.Fatal(err)
		}
		short, err := prepareWith(1)
		if err != nil {
			t.Fatal(err)
		}
		if long != short {
			t.Errorf("format %d: %d allocations with 128 escaped bytes, %d with one", c.format, int(long), int(short))
		}
		if _, err := prepareWith(n + 1); err == nil || errors.Is(err, protocol.ErrInvalid) {
			t.Errorf("format %d: got %v for an identifier over 128 bytes, want the gateway's error", c.format, err)
		}
	}
}
