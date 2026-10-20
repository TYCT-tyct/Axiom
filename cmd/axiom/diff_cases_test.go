//go:build diff

package main

import (
	"slices"
	"strconv"
	"strings"
)

// diffCase is one request sent to both gateways.
type diffCase struct {
	name, path, body string
	key              string // the credential to use instead of the right one
	headers          []string
}

// diffClient is a format a client speaks, and the members of its request for each kind of conversation; {reply} is
// where the request names the reply the upstream gives it (diff_upstream_test.go).
type diffClient struct {
	name, path string
	headers    []string
	members    map[string]string // by kind
}

// diffBackend is a model and the reply the upstream gives to each kind of conversation with it. Axiom's endpoints serve
// the Claude model by Messages, the GPT model by Responses and the DeepSeek-like one by Chat, as new-api's channels do, so
// the upstream sees the same path from both: what is compared is how each relays a reply, not which format it chose to ask for.
type diffBackend struct {
	name, model string
	replies     map[string]string // by kind
}

// png is a picture of one pixel.
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

const weather = `{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`

var diffClients = map[string]diffClient{
	"chat": {
		name: "chat", path: "/v1/chat/completions",
		members: map[string]string{
			"text":      `"messages":[{"role":"user","content":"diff:{reply} Hello"}]`,
			"tools":     `"messages":[{"role":"user","content":"diff:{reply} Weather in Paris?"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":` + weather + `}}]`,
			"reasoning": `"messages":[{"role":"user","content":"diff:{reply} GCD of 1071 and 462?"}],"reasoning_effort":"medium"`,
			"image":     `"messages":[{"role":"user","content":[{"type":"text","text":"diff:{reply} What is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + png + `"}}]}]`,
			"error":     `"messages":[{"role":"user","content":"diff:{reply}"}]`,
			"empty":     ``,
		},
	},
	"messages": {
		name: "messages", path: "/v1/messages", headers: []string{"anthropic-version", "2023-06-01"},
		members: map[string]string{
			"text":      `"max_tokens":256,"messages":[{"role":"user","content":"diff:{reply} Hello"}]`,
			"tools":     `"max_tokens":256,"messages":[{"role":"user","content":"diff:{reply} Weather in San Francisco?"}],"tool_choice":{"type":"any"},"tools":[{"name":"get_weather","description":"Weather","input_schema":` + weather + `}]`,
			"reasoning": `"max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"diff:{reply} GCD of 1071 and 462?"}]`,
			"image":     `"max_tokens":256,"messages":[{"role":"user","content":[{"type":"text","text":"diff:{reply} What is this?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}]}]`,
			"error":     `"max_tokens":256,"messages":[{"role":"user","content":"diff:{reply}"}]`,
			"empty":     `"max_tokens":256`,
		},
	},
	"responses": {
		name: "responses", path: "/v1/responses",
		members: map[string]string{
			"text":      `"input":"diff:{reply} Hello"`,
			"tools":     `"input":"diff:{reply} Weather in Paris?","tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":` + weather + `}]`,
			"reasoning": `"input":"diff:{reply} GCD of 1071 and 462?","reasoning":{"effort":"medium"}`,
			"image":     `"input":[{"role":"user","content":[{"type":"input_text","text":"diff:{reply} What is this?"},{"type":"input_image","image_url":"data:image/png;base64,` + png + `"}]}]`,
			"error":     `"input":"diff:{reply}"`,
			"empty":     ``,
		},
	},
}

var diffBackends = map[string]diffBackend{
	"claude": {name: "claude", model: "claude-opus-5-5", replies: map[string]string{
		"text": "messages-text", "tools": "messages-tools", "reasoning": "messages-thinking", "image": "messages-text",
		"429": "messages-429", "500": "messages-500", "400": "messages-400",
	}},
	"gpt": {name: "gpt", model: "gpt-6-astra", replies: map[string]string{
		"text": "responses-text", "tools": "responses-reasoning", "reasoning": "responses-reasoning", "image": "responses-text",
		"429": "responses-429", "500": "responses-500", "400": "responses-400",
	}},
	"deepseek": {name: "deepseek", model: "deepseek-v4-chat", replies: map[string]string{
		"text": "chat-text", "tools": "chat-tools", "reasoning": "chat-reasoning", "image": "chat-text",
		"429": "chat-429", "500": "chat-500", "400": "chat-400",
	}},
}

// diffPairs are the client formats and the models each is tried with: a format with the model of its own format, which
// both gateways forward as it is, and Chat with each of the other two, which both convert, and with a model of its own
// format, which they forward as it is.
var diffPairs = []struct{ client, backend string }{
	{"chat", "claude"}, {"chat", "gpt"}, {"chat", "deepseek"}, {"messages", "claude"}, {"responses", "gpt"},
}

// body is a request of the client's format to the model, for a conversation, as a stream or whole.
func (c diffClient) body(model string, stream bool, kind, reply string) string {
	return diffBody(model, stream, strings.ReplaceAll(c.members[kind], "{reply}", reply))
}

// diffBody is a request to a model with the members that make it a conversation.
func diffBody(model string, stream bool, members string) string {
	body := `{"model":` + strconv.Quote(model) + `,"stream":` + strconv.FormatBool(stream)
	if members != "" {
		body += "," + members
	}
	return body + "}"
}

// diffCases is every request the two gateways are asked: each pair with each kind of conversation, as a stream and
// whole (text, a tool call, reasoning, a picture, and the three ways an upstream fails), then the ways a request itself
// fails.
func diffCases() []diffCase {
	var cases []diffCase
	failed := map[string]bool{} // the clients whose own failures have been asked, once: they do not depend on the model
	for _, p := range diffPairs {
		client, backend := diffClients[p.client], diffBackends[p.backend]
		prefix := client.name + "-" + backend.name + "-"
		add := func(name, body, key string) {
			cases = append(cases, diffCase{name: prefix + name, path: client.path, body: body, key: key, headers: client.headers})
		}
		for kind, reply := range backend.replies {
			memberKind := kind
			if kind == "429" || kind == "500" || kind == "400" {
				memberKind = "error"
			}
			for _, stream := range []bool{true, false} {
				add(kind+"-"+streamName(stream), client.body(backend.model, stream, memberKind, reply), "")
			}
		}
		if failed[client.name] {
			continue
		}
		failed[client.name] = true
		text := backend.replies["text"]
		add("invalid-json", `{"model":`, "")
		add("missing-members", client.body(backend.model, false, "empty", ""), "")
		add("unknown-model", client.body("no-such-model", false, "text", text), "")
		add("wrong-key", client.body(backend.model, false, "text", text), "sk-not-a-key")
	}
	slices.SortFunc(cases, func(a, b diffCase) int { return strings.Compare(a.name, b.name) })
	return cases
}

func streamName(stream bool) string {
	if stream {
		return "stream"
	}
	return "whole"
}

// failsUpstream reports a case whose upstream answers with a 429 or a 5xx: routing then pauses the endpoint for a few
// seconds (routing.Config.Pause), which would turn every case after it into a 503 that is not what it is about.
func (c diffCase) failsUpstream() bool {
	return strings.Contains(c.name, "-429-") || strings.Contains(c.name, "-500-")
}
