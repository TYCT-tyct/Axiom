package protocol_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

// metadataOf decodes both layers, as a relay must to read the session.
func metadataOf(t *testing.T, body []byte) (device, session string) {
	t.Helper()
	var outer struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := jsonx.Unmarshal(body, &outer); err != nil {
		t.Fatal(err)
	}
	if outer.Metadata.UserID == "" {
		return "", ""
	}
	var inner struct {
		Device  string `json:"device_id"`
		Session string `json:"session_id"`
	}
	if err := jsonx.Unmarshal([]byte(outer.Metadata.UserID), &inner); err != nil {
		t.Fatalf("invalid inner JSON %q: %v", outer.Metadata.UserID, err)
	}
	return inner.Device, inner.Session
}

func TestMetadataEncoding(t *testing.T) {
	for _, format := range []protocol.Format{protocol.Messages, protocol.Chat} {
		for _, value := range []string{"plain", "a\"b", `a\b`, "\x00\n\t", "设备"} {
			t.Run(fmt.Sprintf("%d/%q", format, value), func(t *testing.T) {
				body := metadataRequest(format, "conversation", "hello")
				r := inspected(t, format, body)
				w := protocol.Rewrites{Device: value, Identity: func(string) string { return value }}
				target := protocol.Target{Format: protocol.Messages, Model: "up", MaxOutput: 10}
				u, err := protocol.Prepare(&r, body, target, w)
				if err != nil {
					t.Fatal(err)
				}
				device, session := metadataOf(t, u.Body)
				if device != value || session != value {
					t.Fatalf("got device %q, session %q; want %q", device, session, value)
				}
			})
		}
	}
}

func metadataRequest(format protocol.Format, user, content string) []byte {
	member := `"metadata":{"user_id":%q}`
	if format == protocol.Chat {
		member = `"user":%q`
	}
	return fmt.Appendf(nil, `{"model":"m",`+fmt.Sprintf(member, user)+`,"messages":[{"role":"user","content":%q}]}`, content)
}

func TestMetadataEncodedLength(t *testing.T) {
	for _, format := range []protocol.Format{protocol.Messages, protocol.Chat} {
		// Two backslashes need four inner bytes, so raw component lengths fit while the encoded object does not.
		const encodedLen = userIDLen + 4
		for _, limit := range []int{encodedLen - 1, encodedLen, encodedLen + 1, 0} {
			checkMetadataLength(t, format, limit)
		}
	}
}

func checkMetadataLength(t *testing.T, format protocol.Format, limit int) {
	t.Helper()
	const encodedLen = userIDLen + 4
	body := metadataRequest(format, "conversation", "hello")
	r := inspected(t, format, body)
	w := protocol.Rewrites{Device: `\`, Identity: func(string) string { return `\` }, IdentityLen: limit}
	u, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Messages, Model: "up", MaxOutput: 10}, w)
	if limit == encodedLen-1 {
		if err == nil || errors.Is(err, protocol.ErrInvalid) || !strings.Contains(err.Error(), "IdentityLen") {
			t.Fatalf("format %d, limit %d: want gateway length error, got %v", format, limit, err)
		}
	} else {
		if err != nil {
			t.Fatalf("format %d, limit %d: %v", format, limit, err)
		}
		device, session := metadataOf(t, u.Body)
		if device != `\` || session != `\` {
			t.Fatalf("decoded components changed: %q, %q", device, session)
		}
	}
}
