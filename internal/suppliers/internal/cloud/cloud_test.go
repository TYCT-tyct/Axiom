package cloud

import (
	"errors"
	"testing"
)

// TestRewrite: the members named go, the members added come last in place of any the body had, every other member is
// copied as written, and whether the body asks for a stream is read on the way; a body that is not an object fails.
func TestRewrite(t *testing.T) {
	t.Parallel()
	version := Member{Name: "anthropic_version", Value: []byte(`"v1"`)}
	for _, tc := range []struct {
		name, body, want string
		drop             []string
		add              []Member
		stream           bool
	}{
		{"drop and add", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi é"}],"max_tokens":1e3}`,
			`{"stream":true,"messages":[{"role":"user","content":"hi é"}],"max_tokens":1e3,"anthropic_version":"v1"}`, []string{"model"}, []Member{version}, true},
		{"the stream dropped", `{"model":"m","stream":true}`, `{"anthropic_version":"v1"}`, []string{"model", "stream"}, []Member{version}, true},
		{"the body's own version replaced", `{"anthropic_version":"old","stream":false}`, `{"stream":false,"anthropic_version":"v1"}`, nil, []Member{version}, false},
		{"nothing left", `{"model":"m"}`, `{}`, []string{"model"}, nil, false},
	} {
		got, stream, err := Rewrite([]byte(tc.body), tc.drop, tc.add...)
		if err != nil || string(got) != tc.want || stream != tc.stream {
			t.Errorf("%s: %s, stream %v, %v; want %s, stream %v", tc.name, got, stream, err, tc.want, tc.stream)
		}
	}
	for _, body := range []string{`[]`, `{"a":`, `"x"`, ``} {
		if _, _, err := Rewrite([]byte(body), nil); !errors.Is(err, ErrNotObject) {
			t.Errorf("%q: %v, want ErrNotObject", body, err)
		}
	}
}
