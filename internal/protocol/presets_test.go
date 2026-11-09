package protocol_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func TestPresetMerge(t *testing.T) {
	t.Parallel()
	for _, f := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		t.Run(fmt.Sprint(f), func(t *testing.T) {
			checkPresetMerge(t, f)
		})
	}
}

func checkPresetMerge(t *testing.T, f protocol.Format) {
	t.Helper()
	body := []byte(`{"model":"@preset/support","temperature":null,"provider":{"sort":"price"},"messages":[{"role":"user","content":"hi"}],"input":"hi","tools":[{"type":"function","function":{"name":"a","description":"request"},"name":"a"},{"type":"function","function":{"name":"c"},"name":"c"}]}`)
	config := []byte(`{"model":"m","temperature":0.5,"top_p":0.7,"provider":{"only":["private"]},"system":"be kind","tools":[{"type":"function","function":{"name":"a","description":"preset"},"name":"a"},{"type":"function","function":{"name":"b"},"name":"b"}]}`)
	got, err := protocol.MergePreset(f, body, config, "")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]jsonx.Raw
	if err := jsonx.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if string(out["model"]) != `"m"` || string(out["temperature"]) != "null" || string(out["provider"]) != `{"sort":"price"}` || len(out["top_p"]) == 0 {
		t.Fatalf("merge: %s", got)
	}
	var tools []map[string]jsonx.Raw
	if err := jsonx.Unmarshal(out["tools"], &tools); err != nil || len(tools) != 3 {
		t.Fatalf("tools: %s, %v", got, err)
	}
	checkMergedTools(t, tools)
	if _, err := protocol.Inspect(f, got); err != nil {
		t.Fatal(err)
	}
}

func checkMergedTools(t *testing.T, tools []map[string]jsonx.Raw) {
	t.Helper()
	if !strings.Contains(string(tools[0]["function"]), "request") {
		t.Fatal("request tool failed to replace preset tool")
	}
	for i, name := range []string{`"a"`, `"b"`, `"c"`} {
		if string(tools[i]["name"]) != name {
			t.Fatal("tool union changed order")
		}
	}
}

func TestMessagesFallbacks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		member string
		valid  bool
	}{
		{`"fallbacks":[{"model":"b"},{"model":"c"}]`, true},
		{`"fallbacks":null,"models":["b"]`, true},
		{`"fallbacks":[{"model":"b"}],"models":[]`, false},
		{`"models":[],"fallbacks":[{"model":"b"}]`, false},
		{`"fallbacks":[{"model":"b","temperature":1}]`, false},
		{`"fallbacks":[{}]`, false},
		{`"fallbacks":[{"model":""}]`, false},
		{`"fallbacks":[{"model":"a"},{"model":"b"},{"model":"c"},{"model":"d"}]`, false},
	} {
		t.Run(tc.member, func(t *testing.T) {
			body := []byte(`{"model":"m",` + tc.member + `}`)
			req, err := protocol.Inspect(protocol.Messages, body)
			if !tc.valid {
				if !errors.Is(err, protocol.ErrInvalid) {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(req.Models) == 0 || req.Models[0] != "b" {
				t.Fatalf("models: %v", req.Models)
			}
			checkFallbackForwarding(t, req, body)
		})
	}
}

func checkFallbackForwarding(t *testing.T, req protocol.Request, body []byte) {
	t.Helper()
	up, err := protocol.Prepare(&req, body, protocol.Target{Format: protocol.Messages, Model: "m"}, protocol.Rewrites{})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]jsonx.Raw
	if err := jsonx.Unmarshal(up.Body, &out); err != nil {
		t.Fatal(err)
	}
	if out["fallbacks"] != nil || out["models"] != nil {
		t.Fatalf("leaked routing: %s", up.Body)
	}
}

func TestPresetKeepsOpaqueInput(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"@preset/p","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret","signature":"sig\u006eature"}]}],"custom_number":1e1000,"session_id":"s"}`)
	out, err := protocol.MergePreset(protocol.Messages, body, []byte(`{"model":"m","temperature":0.2}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"signature":"sig\u006eature"`) || !strings.Contains(string(out), `"custom_number":1e1000`) {
		t.Fatalf("opaque values changed: %s", out)
	}
}

func TestSessionIDBoundary(t *testing.T) {
	t.Parallel()
	_, err := protocol.Inspect(protocol.Messages, []byte(`{"model":"m","session_id":"`+strings.Repeat("🙂", 256)+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{`42`, `{}`, `"` + strings.Repeat("x", 257) + `"`, `"` + strings.Repeat("🙂", 257) + `"`} {
		_, err := protocol.Inspect(protocol.Messages, []byte(`{"model":"m","session_id":`+value+`}`))
		if !errors.Is(err, protocol.ErrInvalid) {
			t.Fatalf("accepted session: %s", value)
		}
	}
}

// TestInspectReadsTheEndUser covers where each format names the end user and which member wins: the call log keeps it
// hashed (docs/domains/observability.md「写入时记全」).
func TestInspectReadsTheEndUser(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		format protocol.Format
		body   string
		want   string
	}{
		{"chat user", protocol.Chat, `{"model":"m","messages":[],"user":"u-1"}`, "u-1"},
		{"chat safety identifier alone", protocol.Chat, `{"model":"m","messages":[],"safety_identifier":"s-1"}`, "s-1"},
		{"user wins after the safety identifier", protocol.Responses, `{"model":"m","safety_identifier":"s-1","user":"u-1"}`, "u-1"},
		{"user wins before the safety identifier", protocol.Responses, `{"model":"m","user":"u-1","safety_identifier":"s-1"}`, "u-1"},
		{"a blank user leaves the safety identifier", protocol.Chat, `{"model":"m","messages":[],"safety_identifier":"s-1","user":"  "}`, "s-1"},
		{"messages metadata", protocol.Messages, `{"model":"m","max_tokens":1,"messages":[],"metadata":{"other":[1],"user_id":"m-1"}}`, "m-1"},
		{"messages metadata of another shape", protocol.Messages, `{"model":"m","max_tokens":1,"messages":[],"metadata":"x"}`, ""},
		{"a user that is not a string", protocol.Chat, `{"model":"m","messages":[],"user":42}`, ""},
		{"embeddings", protocol.Embeddings, `{"model":"m","input":"a","user":"e-1"}`, "e-1"},
		{"rerank", protocol.Rerank, `{"model":"m","query":"q","documents":["d"],"user":"r-1"}`, "r-1"},
		{"completions safety identifier", protocol.Completions, `{"model":"m","prompt":"p","safety_identifier":"c-1"}`, "c-1"},
		{"none", protocol.Chat, `{"model":"m","messages":[]}`, ""},
	} {
		got, err := protocol.Inspect(tc.format, []byte(tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.EndUser != tc.want {
			t.Errorf("%s: end user %q, want %q", tc.name, got.EndUser, tc.want)
		}
	}
}

func TestPresetNamedTools(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tools, request string
		count          int
	}{
		{`[{"type":"custom","name":"a"},{"type":"custom","name":"b"}]`, `[{"type":"custom","name":"a","description":"override"}]`, 2},
		{`[{"type":"openrouter:advisor","parameters":{"name":"a"}},{"type":"openrouter:advisor","parameters":{"name":"b"}}]`, `[{"type":"openrouter:advisor","parameters":{"name":"a","instructions":"override"}}]`, 2},
		{`[{"type":"openrouter:web_search"}]`, `[{"type":"openrouter:web_search","parameters":{"engine":"override"}}]`, 1},
	} {
		body := []byte(`{"model":"@preset/p","tools":` + tc.request + `}`)
		config := []byte(`{"model":"m","tools":` + tc.tools + `}`)
		got := must(protocol.MergePreset(protocol.Responses, body, config, ""))
		var out struct {
			Tools []jsonx.Raw `json:"tools"`
		}
		if err := jsonx.Unmarshal(got, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Tools) != tc.count || !strings.Contains(string(out.Tools[0]), "override") {
			t.Fatalf("named tools lost: %s", got)
		}
	}
}

func TestPresetModelList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		member, model string
		alternates    int
	}{
		{``, "a", 1}, {`,"models":["b"]`, "b", 0},
	} {
		body := []byte(`{"model":"@preset/list"` + tc.member + `}`)
		got := must(protocol.MergePreset(protocol.Chat, body, []byte(`{"models":["a","b"]}`), ""))
		req := must(protocol.Inspect(protocol.Chat, got))
		if req.Model != tc.model || len(req.Models) != tc.alternates {
			t.Fatalf("model list: %+v", req)
		}
	}
}

func TestPresetMergeErrorsAreInvalidRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ body, config string }{
		{`{"model":"@preset/p","tools":[{"type":"function","name":"f"}]}`, `{"model":"m","tools":{"a":1}}`},
		{`{"model":"@preset/p","tools":[{"type":"function","name":"f"}]}`, `{"model":"m","tools":[1]}`},
		{`{"model":"@preset/p"}`, `{"model":"m","system":"help"}`},
	} {
		_, err := protocol.MergePreset(protocol.Chat, []byte(tc.body), []byte(tc.config), "")
		if !errors.Is(err, protocol.ErrInvalid) {
			t.Fatalf("merge error is not a request error: %v", err)
		}
	}
}

func TestPresetMessagesFallbackOverride(t *testing.T) {
	t.Parallel()
	for _, config := range []string{`{"models":["a","b"]}`, `{"models":["a"]}`, `{"model":"a","models":["b"]}`} {
		for _, tc := range []struct {
			fallbacks string
			models    []string
		}{
			{`[{"model":"c"}]`, []string{"c"}},
			{`[]`, nil},
		} {
			body := []byte(`{"model":"@preset/p","fallbacks":` + tc.fallbacks + `}`)
			got := must(protocol.MergePreset(protocol.Messages, body, []byte(config), ""))
			req := must(protocol.Inspect(protocol.Messages, got))
			if req.Model != "a" || !slices.Equal(req.Models, tc.models) {
				t.Fatalf("fallback override: %+v", req)
			}
		}
	}
}

func TestPresetCacheControlDoesNotReachUpstream(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"@preset/support","messages":[{"role":"user","content":"hi"}]}`)
	config := []byte(`{"model":"m","cache_enabled":true,"cache_ttl_seconds":60}`)
	got, err := protocol.MergePreset(protocol.Chat, body, config, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "cache_enabled") || strings.Contains(string(got), "cache_ttl_seconds") {
		t.Fatalf("cache control reached the upstream body: %s", got)
	}
}
