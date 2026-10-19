//go:build diff

package main

import "regexp"

// ruleStatus says what a difference that a rule matches is.
type ruleStatus int

const (
	// allowed is a difference Axiom has on purpose, listed in docs/domains/gateway.md「与 new-api 的有意差异」.
	allowed ruleStatus = iota
	// open is a difference nobody has ruled on yet: it is neither fixed nor decided, and is waiting for its issue.
	open
)

// diffRule explains the differences whose path matches, in the cases whose name matches.
type diffRule struct {
	path, cases *regexp.Regexp
	status      ruleStatus
	reason      string
	hits        int
}

func (r *diffRule) matches(name string, d difference) bool {
	return r.path.MatchString(d.path) && (r.cases == nil || r.cases.MatchString(name))
}

// diffRules is the allowlist and the list of open differences. A rule that matches nothing in a run fails it: a
// difference that has been fixed must leave the list, or the list would one day excuse the difference coming back.
func diffRules() []*diffRule {
	rule := func(status ruleStatus, path, cases, reason string) *diffRule {
		r := &diffRule{path: regexp.MustCompile("^(?:" + path + ")$"), status: status, reason: reason}
		if cases != "" {
			r.cases = regexp.MustCompile("^(?:" + cases + ")$")
		}
		return r
	}
	return []*diffRule{
		// What is not decided yet comes first, so that a broader rule below does not take its differences.
		rule(open, `json\.error\.message`, `.*-400-.*`,
			"a request the upstream rejects: new-api passes the upstream's text on, Axiom says only that the request is not valid (gateway.md「待定问题」: 保留原文)"),
		rule(open, `json\.error\.message`, `.*-invalid-json`,
			"Axiom answers an unparsable body with the JSON reader's own message (jsontext: unexpected EOF ...), new-api with a plain sentence"),
		rule(open, `json\.error\.(code|param|type|metadata\.error_type)`, `(chat|responses)-.*`,
			"Chat and Responses errors: OpenAI's and new-api's have a string error.code, error.type and error.param, Axiom's follow OpenRouter's with the HTTP status as error.code and the type in error.metadata.error_type"),
		rule(open, `header\.x-accel-buffering`, "",
			"new-api marks every event stream X-Accel-Buffering: no, which stops an nginx in front from buffering it; Axiom sends no such header"),
		rule(open, `json\.choices\[\]\.message\.reasoning(_content)?|chat\.choices\[\]\.delta\.reasoning(_content)?`, "",
			"reasoning text in a Chat reply: new-api uses reasoning_content (DeepSeek's name, which many clients read), Axiom uses reasoning (OpenRouter's)"),
		rule(open, `sse\[response\.(created|in_progress)\]\.response\.usage`, "",
			"OpenAI's Responses events carry usage: null until the response ends; Axiom leaves the member out"),

		rule(allowed, `header\.(x-oneapi-request-id|x-new-api-version|x-request-id|x-generation-id)`, "",
			"each gateway names its own request id; Axiom's is the call id (gateway.md「调用编号」)"),
		rule(allowed, `header\.(cf-ray|openai-organization|x-upstream)`, "",
			"new-api relays the upstream's headers; Axiom lets through only its own (gateway.md「转发规则」: 响应头按白名单放行)"),
		rule(allowed, `json\.id|chat\.id|sse\[message_start\]\.message\.id|sse\[response\.[a-z_]+\]\.response\.id`, "",
			"the id of a reply is Axiom's call id, new-api passes the upstream's (gateway.md「转发规则」: 自己回显)"),
		rule(allowed, `(json|chat)\.usage\.cost(_currency|_estimated)?|sse\[message_delta\]\.usage\.cost(_currency|_estimated)?|`+
			`sse\[response\.completed\]\.response\.usage\.cost(_currency|_estimated)?`, "",
			"Axiom's usage carries the call's cost (gateway.md「用量与费用」)"),
		rule(allowed, `(json|chat)\.usage\.(billing_usage\..*|usage_semantic|usage_source|claude_cache_creation_.*|input_tokens|output_tokens|`+
			`input_tokens_details|(prompt|completion)_tokens_details\.(audio|image|text)_tokens)`, `chat-.*`,
			"members new-api adds to a Chat usage that are not part of the format"),
		rule(allowed, `(json|chat)\.usage\.prompt_tokens_details\.cache_write_tokens`, "",
			"OpenRouter's usage reports cache writes, which Axiom's does the same way (gateway.md「用量与费用」)"),
		rule(allowed, `json\.choices\[\]\.(logprobs|message\.refusal)|chat\.system_fingerprint`, "",
			"members that OpenAI's reply has as null: Axiom writes them, new-api's conversion writes some of them or none"),
		rule(allowed, `json\.choices\[\]\.message\.content`, `chat-gpt-(reasoning|tools)-whole`,
			"a message of tool calls only has content null in OpenAI's reply, new-api writes an empty string"),
		rule(allowed, `(json|chat)\.created`, "chat-.*",
			"a Chat reply made from another format is stamped with the time of the call; new-api stamps the upstream's creation time or its own clock"),
		rule(allowed, `json\.choices\[\]\.message\.reasoning_details.*|chat\.choices\[\]\.delta\.reasoning_details`, "",
			"OpenRouter's reasoning_details, which carry the signature and the encrypted content that a later request sends back"),
		rule(allowed, `(json|chat)\.usage\.(prompt_tokens|completion_tokens|total_tokens)`, `(chat|messages)-claude-reasoning-.*`,
			"the upstream sample reports no usage, so each gateway counts the tokens itself"),
		rule(allowed, `(json|chat)\.usage\.completion_tokens_details\.reasoning_tokens`, `chat-gpt-(reasoning|tools)-.*`,
			"new-api's Responses-to-Chat conversion reports no reasoning tokens, Axiom passes the upstream's count on"),
		rule(allowed, `json\.choices\[\]\.message\.tool_calls\[\]\.function\.arguments`, `chat-claude-tools-whole`,
			"Axiom builds a whole reply from the stream, so the arguments are the stream's text; new-api writes the object it was given again"),
		rule(allowed, `sse\[ping\]`, "",
			"nothing is sent before the first content, and a ping before it is dropped (gateway.md「保活」)"),
		rule(allowed, `sse\[message_delta\]\.usage\.input_tokens`, "",
			"new-api repeats the input tokens of message_start in message_delta; Axiom sends the upstream's usage as it came"),
		rule(allowed, `header\.(content-type|cache-control)`, "",
			"JSON without a charset and event streams with utf-8 in Axiom; and new-api answers a failed streaming request as an event stream around a JSON error"),
		rule(allowed, `status`, `.*-(429|500)-.*`,
			"the upstream's own 429 and 5xx are never the client's: every attempt failed is 502 upstream_error (gateway.md「错误」)"),
		rule(allowed, `status`, `.*-unknown-model`,
			"a model that does not exist is 404 model_not_found, as OpenRouter has it; new-api answers 503 (gateway.md「决策与理由」)"),
		rule(allowed, `json\.type|json\.error\.(error_type|type|code|message)`, `messages-.*`,
			"Anthropic's own error envelope, with the unified error_type; new-api's lacks the type member and puts its own type and wording in"),
		rule(allowed, `json\.error\.message`, `(chat|responses)-.*`,
			"Axiom's errors use its own unified wording (gateway.md「错误」)"),
	}
}
