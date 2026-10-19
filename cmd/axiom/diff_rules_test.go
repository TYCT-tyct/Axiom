//go:build diff

package main

import "regexp"

// diffRule explains a difference Axiom has on purpose, listed in docs/domains/gateway.md「与 new-api 的有意差异」: the
// differences whose path matches, in the cases whose name matches.
type diffRule struct {
	path, cases *regexp.Regexp
	reason      string
	hits        int
}

func (r *diffRule) matches(name string, d difference) bool {
	return r.path.MatchString(d.path) && (r.cases == nil || r.cases.MatchString(name))
}

// diffRules is the allowlist. A rule that matches nothing in a run fails it: a difference that has been fixed must leave
// the list, or the list would one day excuse the difference coming back. A difference nobody has ruled on yet is not
// listed: it fails the run until its issue decides it.
func diffRules() []*diffRule {
	rule := func(path, cases, reason string) *diffRule {
		r := &diffRule{path: regexp.MustCompile("^(?:" + path + ")$"), reason: reason}
		if cases != "" {
			r.cases = regexp.MustCompile("^(?:" + cases + ")$")
		}
		return r
	}
	return []*diffRule{
		rule(`header\.(x-oneapi-request-id|x-new-api-version|x-request-id|x-generation-id)`, "",
			"each gateway names its own request id; Axiom's is the call id (gateway.md「调用编号」)"),
		rule(`header\.(cf-ray|openai-organization|x-upstream)`, "",
			"new-api relays the upstream's headers; Axiom lets through only its own (gateway.md「转发规则」: 响应头按白名单放行)"),
		rule(`json\.id|chat\.id|sse\[message_start\]\.message\.id|sse\[response\.[a-z_]+\]\.response\.id`, "",
			"the id of a reply is Axiom's call id, new-api passes the upstream's (gateway.md「转发规则」: 自己回显)"),
		rule(`(json|chat)\.usage\.cost(_currency|_estimated)?|sse\[message_delta\]\.usage\.cost(_currency|_estimated)?|`+
			`sse\[response\.completed\]\.response\.usage\.cost(_currency|_estimated)?`, "",
			"Axiom's usage carries the call's cost (gateway.md「用量与费用」)"),
		rule(`(json|chat)\.usage\.(billing_usage\..*|usage_semantic|usage_source|claude_cache_creation_.*|input_tokens|output_tokens|`+
			`input_tokens_details|(prompt|completion)_tokens_details\.(audio|image|text)_tokens)`, `chat-.*`,
			"members new-api adds to a Chat usage that are not part of the format"),
		rule(`(json|chat)\.usage\.prompt_tokens_details\.cache_write_tokens`, "",
			"OpenRouter's usage reports cache writes, which Axiom's does the same way (gateway.md「用量与费用」)"),
		rule(`json\.choices\[\]\.(logprobs|message\.refusal)|chat\.system_fingerprint`, "",
			"members that OpenAI's reply has as null: Axiom writes them, new-api's conversion writes some of them or none"),
		rule(`json\.choices\[\]\.message\.content`, `chat-gpt-(reasoning|tools)-whole`,
			"a message of tool calls only has content null in OpenAI's reply, new-api writes an empty string"),
		rule(`(json|chat)\.created`, "chat-.*",
			"a Chat reply made from another format is stamped with the time of the call; new-api stamps the upstream's creation time or its own clock"),
		rule(`json\.choices\[\]\.message\.reasoning_details.*|chat\.choices\[\]\.delta\.reasoning_details`, "",
			"OpenRouter's reasoning_details, which carry the signature and the encrypted content that a later request sends back"),
		rule(`(json|chat)\.usage\.(prompt_tokens|completion_tokens|total_tokens)`, `(chat|messages)-claude-reasoning-.*`,
			"the upstream sample reports no usage, so each gateway counts the tokens itself"),
		rule(`(json|chat)\.usage\.completion_tokens_details\.reasoning_tokens`, `chat-gpt-(reasoning|tools)-.*`,
			"new-api's Responses-to-Chat conversion reports no reasoning tokens, Axiom passes the upstream's count on"),
		rule(`json\.choices\[\]\.message\.tool_calls\[\]\.function\.arguments`, `chat-claude-tools-whole`,
			"Axiom builds a whole reply from the stream, so the arguments are the stream's text; new-api writes the object it was given again"),
		rule(`sse\[ping\]`, "",
			"nothing is sent before the first content, and a ping before it is dropped (gateway.md「保活」)"),
		rule(`sse\[message_delta\]\.usage\.input_tokens`, "",
			"new-api repeats the input tokens of message_start in message_delta; Axiom sends the upstream's usage as it came"),
		rule(`header\.(content-type|cache-control)`, "",
			"JSON without a charset and event streams with utf-8 in Axiom; and new-api answers a failed streaming request as an event stream around a JSON error"),
		rule(`status`, `.*-(429|500)-.*`,
			"the upstream's own 429 and 5xx are never the client's: every attempt failed is 502 upstream_error (gateway.md「错误」)"),
		rule(`status`, `.*-unknown-model`,
			"a model that does not exist is 404 model_not_found, as OpenRouter has it; new-api answers 503 (gateway.md「决策与理由」)"),
		rule(`header\.x-accel-buffering|json\..*`, `.*-(400|429|500)-stream`,
			"new-api answers a failed streaming request as an event stream, whose events are not compared here, so it has the stream's headers and no JSON; Axiom fails before any stream starts, with the JSON error (gateway.md「转发规则」)"),
		rule(`json\.type|json\.error\.(error_type|type|code)`, `messages-.*`,
			"Anthropic's own error envelope, with the unified error_type; new-api's lacks the type member and puts its own type in"),
		rule(`json\.error\.message`, `(chat|responses|messages)-.*-(429|500)-whole|(chat|responses|messages)-.*-(missing-members|unknown-model|wrong-key)`,
			"the errors Axiom makes use its own unified wording (gateway.md「错误」); the upstream's own 429 and 5xx are not passed on"),
		rule(`json\.error\.metadata\..*`, `(chat|responses)-.*`,
			"the unified error_type, which is Axiom's own and new-api has no counterpart for (gateway.md「错误」)"),
		rule(`json\.error\.type`, `(chat|responses)-.*-(invalid-json|missing-members|unknown-model|wrong-key)`,
			"new-api puts its own name, new_api_error, in the type of the errors it makes; Axiom's is the type of the format's clients, as Messages' (gateway.md「错误」)"),
		rule(`json\.error\.code`, `(chat|responses)-.*-(invalid-json|wrong-key)`,
			"new-api leaves the code empty for a body that is not JSON and for a wrong key; Axiom's is the unified error type"),
		rule(`json\.error\.(type|code)`, `(chat|responses)-.*-(429|500)-whole`,
			"an upstream's 429 or 5xx is Axiom's own 502 upstream_error, so its type and code are Axiom's; new-api passes the upstream's on"),
		rule(`json\.choices\[\]\.message\.reasoning|chat\.choices\[\]\.delta\.reasoning`, "",
			"Axiom writes reasoning, OpenRouter's name, beside reasoning_content, new-api's and DeepSeek's, with the same text (gateway.md「转发规则」)"),
	}
}
