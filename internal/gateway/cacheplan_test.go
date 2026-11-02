package gateway

import (
	"net/http"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

func TestCachePlanPrecedence(t *testing.T) {
	t.Parallel()
	on, off := true, false
	body := []byte(`{"model":"acme/alpha","messages":[{"role":"user","content":"hi"}]}`)
	tests := []struct {
		name    string
		preset  *bool
		ttl     int64
		header  http.Header
		want    bool
		wantTTL int64
		clear   bool
	}{
		{name: "nothing asks", header: http.Header{}, want: false},
		{name: "header on", header: http.Header{"X-Openrouter-Cache": {"true"}}, want: true, wantTTL: 300},
		{name: "header off", preset: &on, header: http.Header{"X-Openrouter-Cache": {"false"}}, want: false},
		{name: "preset on", preset: &on, header: http.Header{}, want: true, wantTTL: 300},
		{name: "preset off blocks header", preset: &off, header: http.Header{"X-Openrouter-Cache": {"TRUE"}}, want: false},
		{name: "preset off leaves it off", preset: &off, header: http.Header{}, want: false},
		{name: "preset ttl", preset: &on, ttl: 60, header: http.Header{}, want: true, wantTTL: 60},
		{name: "header ttl wins", preset: &on, ttl: 60, header: http.Header{"X-Openrouter-Cache-Ttl": {"120"}}, want: true, wantTTL: 120},
		{name: "clear", header: http.Header{"X-Openrouter-Cache": {"true"}, "X-Openrouter-Cache-Clear": {"true"}}, want: true, wantTTL: 300, clear: true},
		{name: "invalid switch", header: http.Header{"X-Openrouter-Cache": {"yes"}}, want: false},
		{name: "zero ttl clamps", header: http.Header{"X-Openrouter-Cache": {"true"}, "X-Openrouter-Cache-Ttl": {"0"}}, want: true, wantTTL: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &Call{Format: protocol.Chat, Request: protocol.Request{Stream: true}, Body: body, presetCache: tc.preset, presetTTL: tc.ttl}
			got := cachePlanFor(c, tc.header)
			if got.enabled != tc.want || int64(got.ttl.Seconds()) != tc.wantTTL || got.clear != tc.clear {
				t.Errorf("enabled %v ttl %s clear %v, want %v %d %v", got.enabled, got.ttl, got.clear, tc.want, tc.wantTTL, tc.clear)
			}
		})
	}
}

func TestCachePlanLeavesUncacheableRequestsOff(t *testing.T) {
	t.Parallel()
	h := http.Header{"X-Openrouter-Cache": {"true"}}
	for name, c := range map[string]*Call{
		"direct":        {Format: protocol.Embeddings},
		"compact":       {Format: protocol.Responses, Compact: true},
		"previous":      {Format: protocol.Responses, Request: protocol.Request{Previous: "resp_1"}},
		"compacts":      {Format: protocol.Responses, Request: protocol.Request{Compacts: true}},
		"file refs":     {Format: protocol.Chat, Request: protocol.Request{FileRefs: 1}},
		"native search": {Format: protocol.Responses, Request: protocol.Request{Needs: protocol.WebSearch}},
		"native code":   {Format: protocol.Responses, Request: protocol.Request{Needs: protocol.CodeExecution}},
		"native file":   {Format: protocol.Responses, Request: protocol.Request{Needs: protocol.FileSearch}},
	} {
		if got := cachePlanFor(c, h); got.enabled {
			t.Errorf("%s was cacheable", name)
		}
	}
}

func TestParseCacheTTL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"60", 60, true},
		{"60abc", 60, true},
		{"1.5", 1, true},
		{"0", 1, true},
		{"999999999999999999999", 86400, true},
		{"86400", 86400, true},
		{"86401", 86400, true},
		{"-5", 0, false},
		{"abc", 0, false},
		{"", 0, false},
		{" 60 ", 60, true},
	} {
		got, ok := parseCacheTTL(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parse %q = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWriteNormalized(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{` { "a" : 1 , "b" : [ 2, 3 ] } `, `{"a":1,"b":[2,3]}`},
		{`{"a":" x y "}`, `{"a":" x y "}`},
		{`{"a":"x\" y"}`, `{"a":"x\" y"}`},
		{`{"b":1,"a":2}`, `{"b":1,"a":2}`},
		{`{"a":1,"a":2}`, `{"a":1,"a":2}`},
		{`{"a":1.0}`, `{"a":1.0}`},
	} {
		var got strings.Builder
		writeNormalized(&got, []byte(tc.in))
		if got.String() != tc.want {
			t.Errorf("normalize %q = %q, want %q", tc.in, got.String(), tc.want)
		}
	}
}
