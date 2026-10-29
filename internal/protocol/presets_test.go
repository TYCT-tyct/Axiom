package protocol_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func TestPresetMerge(t *testing.T) {
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

func TestPresetNamedTools(t *testing.T) {
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
