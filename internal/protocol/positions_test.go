package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestInspectRejectsDuplicateAfterLargePayload(t *testing.T) {
	t.Parallel()
	for _, tail := range []string{
		`,"opaque":{"key":1,"k\u0065y":2}}`,
		`,"m\u006fdel":"other"}`,
	} {
		body := []byte(`{"model":"m","input":"` + strings.Repeat("x", 4<<20) + `"` + tail)
		if _, err := Inspect(Responses, body); !errors.Is(err, ErrInvalid) {
			t.Fatalf("got %v, want duplicate-key rejection", err)
		}
	}
}

func TestPositionCapacity(t *testing.T) {
	t.Parallel()
	for f, rules := range rewriteRules {
		names := map[string]struct{}{}
		for _, members := range []map[string]rule{rules.members, compactRules[f].members} {
			for name := range members {
				names[name] = struct{}{}
			}
		}
		if len(names) > len((requestPositions{}).members) {
			t.Fatalf("format %d has %d distinct rules: position table must cover them", f, len(names))
		}
	}
}

func TestPrepareDiscardsPositionsForDifferentBodySize(t *testing.T) {
	t.Parallel()
	for f := range Format(len(rewriteRules)) {
		for _, bodies := range [][2]string{
			{`{"model":"original","provider":{},"input":"content"}`, `{"model":"b"}`},
			{`{"model":"original","provider":{"order":["one"]},"axiom":{},"input":"content"}`, `{"provider":{},"model":"b"}`},
			{`{"model":"a"}`, `{"axiom":{},"input":"longer content","model":"other","provider":{}}`},
		} {
			t.Run(fmt.Sprintf("format=%d/size=%d-to-%d", f, len(bodies[0]), len(bodies[1])), func(t *testing.T) {
				r, err := Inspect(f, []byte(bodies[0]))
				if err != nil {
					t.Fatal(err)
				}
				target := Target{Format: f, Model: "up", MaxOutput: 7000, Streams: true}
				got, gotErr := Prepare(&r, []byte(bodies[1]), target, Rewrites{})
				r.positions = requestPositions{}
				want, wantErr := Prepare(&r, []byte(bodies[1]), target, Rewrites{})
				if gotErr != nil || wantErr != nil || !bytes.Equal(got.Body, want.Body) {
					t.Fatalf("stale positions: %s (%v)\ntokenized: %s (%v)", got.Body, gotErr, want.Body, wantErr)
				}
			})
		}
	}
}

// The tokenizer remains the oracle for indexed rewrites, including byte preservation,
// nested type errors, deletion commas and history insertion.
func FuzzIndexedRewrite(f *testing.F) {
	for format, name := range []string{"messages/text", "responses/text", "chat/text"} {
		body, err := fs.ReadFile(os.DirFS("testdata"), name+"/request.json")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(uint8(format), string(body), false)
	}
	for _, body := range []string{
		` {"provider":{}, "plugins":[], "opaque":1,"model":"m","input":[] } `,
		`{"model":"m","route":{},"input":"x","provider":{}}`,
		`{"model":"m","include":["x"],"metadata":{"user_id":"u","x":{}},"stream_options":{"other":false}}`,
		`{"model":"m","m\u0065tadata":{"user_id":[]},"user":1}`,
		`{"model":"m","previous_response_id":"r","input":[{"content":"x"}]}`,
	} {
		for format := range uint8(3) {
			f.Add(format, body, false)
			f.Add(format, body, true)
		}
	}
	f.Fuzz(func(t *testing.T, format uint8, body string, compact bool) {
		r, err := Inspect(Format(format%3), []byte(body))
		if err != nil {
			return
		}
		target := Target{Format: r.Format, Model: "up", Streams: true, MaxOutput: 7000, Compact: compact}
		w := Rewrites{Identity: func(s string) string { return "h(" + s + ")" }}
		if r.Previous != "" {
			w.History = [][]byte{[]byte(`{"type":"message","role":"user","content":"old"}`)}
		}
		got, gotErr := Prepare(&r, []byte(body), target, w)
		r.positions = requestPositions{}
		want, wantErr := Prepare(&r, []byte(body), target, w)
		if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || !bytes.Equal(got.Body, want.Body) {
			t.Fatalf("indexed: %s (%v)\ntokenized: %s (%v)", got.Body, gotErr, want.Body, wantErr)
		}
	})
}
