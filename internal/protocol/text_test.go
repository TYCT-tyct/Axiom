package protocol_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

func TestText(t *testing.T) {
	t.Parallel()
	payload := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("opaque bytes", 4000)))
	for _, tc := range []struct{ name, body, want string }{
		{"empty object", `{}`, ""},
		{"empty values", `{"input":"","other":[null,false,42]}`, ""},
		{"values only", `{"model":"m","messages":[{"role":"user","content":"hi"}],"n":123,"stream":true}`, "muserhi"},
		{"Chinese UTF-8", `{"model":"m","content":"你好"}`, "m你好"},
		{"Chinese escaped", `{"model":"m","content":"\u4f60\u597d"}`, "m你好"},
		{"audio", `{"model":"m","input_audio":{"data":"` + payload + `"}}`, "m"},
		{"encrypted reasoning", `{"model":"m","input":[{"encrypted_content":"` + payload + `"}]}`, "m"},
		{"signature", `{"model":"m","messages":[{"signature":"` + payload + `"}]}`, "m"},
		{"escaped opaque key", `{"model":"m","\u0073ignature":"` + payload + `"}`, "m"},
		{"media", `{"model":"m","content":[{"type":"image","source":{"data":"` + payload + `"}}]}`, "mimage"},
		{"file", `{"model":"m","file_data":"` + payload + `"}`, "m"},
		{"image URL", `{"model":"m","image_url":"data:image/png;base64,` + payload + `"}`, "m"},
		{"tool schema", `{"model":"m","tools":[{"name":"lookup","description":"find a place","schema":{"type":"string"}}]}`, "mlookupfind a placestring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var text strings.Builder
			remaining, err := protocol.Text([]byte(tc.body), 256<<10, func(piece []byte) bool {
				text.Write(piece)
				return true
			})
			if err != nil || remaining != 0 || text.String() != tc.want {
				t.Fatalf("text %.80q (%d bytes), remaining %d, error %v; want %q", text.String(), text.Len(), remaining, err, tc.want)
			}
		})
	}
}

func TestTextByteLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		literal, want string
		budget        int
		remaining     int64
	}{
		{`"你好"`, "你", 3, 3},
		{`"\u4f60\u597d"`, "你", 6, 6},
		{`"\uD83D\uDE42x"`, "", 11, 13},
		{`"\uD83D\uDE42x"`, "🙂", 12, 1},
		{`"🙂x"`, "", 3, 5},
		{`"a\nb"`, "a\n", 3, 1},
		{`"a"`, "", 0, 1},
	} {
		var text strings.Builder
		remaining, err := protocol.Text([]byte(`{"value":`+tc.literal+`}`), tc.budget, func(piece []byte) bool {
			text.Write(piece)
			return true
		})
		if err != nil || text.String() != tc.want || remaining != tc.remaining {
			t.Errorf("%s at %d: %q, %d, %v; want %q, %d", tc.literal, tc.budget, text.String(), remaining, err, tc.want, tc.remaining)
		}
	}
}

func TestTextStops(t *testing.T) {
	t.Parallel()
	var calls int
	_, err := protocol.Text([]byte(`{"model":"m","content":"hi"}`), 100, func([]byte) bool { calls++; return false })
	if err != nil || calls != 1 {
		t.Fatalf("calls %d, error %v", calls, err)
	}
	for _, body := range [][]byte{nil, {}, []byte(`{"model":`)} {
		_, err = protocol.Text(body, 100, func([]byte) bool { return true })
		if !errors.Is(err, protocol.ErrInvalid) {
			t.Fatalf("%q: want ErrInvalid, got %v", body, err)
		}
	}
}

func checkTextBytes(t *testing.T, body []byte, bound int64) {
	t.Helper()
	var text int64
	remaining, err := protocol.Text(body, 256<<10, func(piece []byte) bool { text += int64(len(piece)); return true })
	if err != nil || text+remaining > bound {
		t.Fatalf("decoded text %d, remaining %d, bound %d, error %v", text, remaining, bound, err)
	}
}
