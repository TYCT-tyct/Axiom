package protocol_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/protocol"
)

// TestRequestParameters: provider.require_parameters reads the sampling, output-cap and stopping parameters a request
// sets, treats a null as absent, and names the first member whose support the catalog does not declare. Controls and
// capability parameters are not read here.
func TestRequestParameters(t *testing.T) {
	t.Parallel()
	set := func(params ...protocol.Parameter) (s protocol.ParameterSet) {
		for _, p := range params {
			s.Add(p)
		}
		return s
	}
	for name, tc := range map[string]struct {
		body    string
		want    protocol.ParameterSet
		unknown string
	}{
		"nothing":            {`{"model":"m","messages":[]}`, 0, ""},
		"a parameter":        {`{"temperature":0.5}`, set(protocol.ParamTemperature), ""},
		"both sampling":      {`{"temperature":0.5,"top_p":0.9}`, set(protocol.ParamTemperature, protocol.ParamTopP), ""},
		"an output cap":      {`{"max_output_tokens":10}`, set(protocol.ParamMaxTokens), ""},
		"an alias":           {`{"stop_sequences":["END"]}`, set(protocol.ParamStop), ""},
		"a null is absent":   {`{"temperature":null}`, 0, ""},
		"a control":          {`{"metadata":{"user_id":"u"},"stream":true}`, 0, ""},
		"a nested parameter": {`{"messages":[{"temperature":1}]}`, 0, ""},
		"capability names":   {`{"tools":[],"response_format":{"type":"json_object"},"reasoning":{"effort":"low"}}`, 0, ""},
		"a null unknown":     {`{"seed":null}`, 0, ""},
		"an unknown":         {`{"seed":7}`, 0, "seed"},
		"the first unknown":  {`{"seed":7,"logprobs":true}`, 0, "seed"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, unknown := protocol.RequestParameters([]byte(tc.body))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parameters (-want +got):\n%s", diff)
			}
			if unknown != tc.unknown {
				t.Errorf("unknown %q, want %q", unknown, tc.unknown)
			}
		})
	}
}

// TestRequestParametersBoundsAName: a refused name is the client's and may be long; the message repeats at most 64
// bytes of it, ending mid-rune safely.
func TestRequestParametersBoundsAName(t *testing.T) {
	t.Parallel()
	long := make([]byte, 80)
	for i := range long {
		long[i] = 'a'
	}
	for name, text := range map[string]string{
		"ascii":    string(long),
		"mid-rune": strings.Repeat("é", 33), // 66 bytes; the 64-byte cut splits the last one
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, unknown := protocol.RequestParameters([]byte(`{"` + text + `":true}`))
			if len(unknown) > 64+len("…") || unknown[len(unknown)-len("…"):] != "…" || !utf8.ValidString(unknown) {
				t.Errorf("unknown %q, want at most 64 valid bytes and an ellipsis", unknown)
			}
		})
	}
}
