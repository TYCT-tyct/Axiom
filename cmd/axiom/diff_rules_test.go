//go:build diff

package main

import (
	"encoding/json/v2" //nolint:depguard // reads values of replies; jsonx guards input from outside
	"regexp"
)

// ruleStatus says what a difference that a rule matches is.
type ruleStatus int

const (
	// allowed is a difference Axiom has on purpose, listed in docs/domains/gateway.md「与 new-api 的有意差异」.
	allowed ruleStatus = iota
	// open is a difference nobody has ruled on yet: it is neither fixed nor decided, and is waiting for its issue.
	open
)

// diffRule explains the differences whose path matches, in the cases whose name matches, and whose two values are the
// ones the reason is about: a rule is for one difference, not for whatever else happens at the same path.
type diffRule struct {
	path, cases *regexp.Regexp
	when        func(difference) bool
	status      ruleStatus
	reason      string
	hits        int
}

func (r *diffRule) matches(name string, d difference) bool {
	return r.path.MatchString(d.path) && (r.cases == nil || r.cases.MatchString(name)) && r.when(d)
}

// explain is the first rule that explains the difference, and counts it.
func explain(rules []*diffRule, name string, d difference) *diffRule {
	for _, r := range rules {
		if r.matches(name, d) {
			r.hits++
			return r
		}
	}
	return nil
}

// Conditions on the two values of a difference. A value is the text of its JSON leaf (a string is quoted), or the
// header or the status as it is; absent is the member the other reply has and this one lacks.
func axiomLacks(d difference) bool  { return d.axiom == absent }
func newAPILacks(d difference) bool { return d.newAPI == absent }

func values(newAPI, axiom string) func(difference) bool {
	return func(d difference) bool { return d.newAPI == newAPI && d.axiom == axiom }
}

func axiomIs(pattern string) func(difference) bool {
	re := regexp.MustCompile("^(?:" + pattern + ")$")
	return func(d difference) bool { return re.MatchString(d.axiom) }
}

func bothAre(pattern string) func(difference) bool {
	re := regexp.MustCompile("^(?:" + pattern + ")$")
	return func(d difference) bool { return re.MatchString(d.newAPI) && re.MatchString(d.axiom) }
}

func all(conditions ...func(difference) bool) func(difference) bool {
	return func(d difference) bool {
		for _, c := range conditions {
			if !c(d) {
				return false
			}
		}
		return true
	}
}

// sameJSONText holds when both values are JSON strings whose text is the same JSON document: the same arguments of a
// tool call, however the spaces fall.
func sameJSONText(d difference) bool {
	var a, b string
	if json.Unmarshal([]byte(d.newAPI), &a) != nil || json.Unmarshal([]byte(d.axiom), &b) != nil {
		return false
	}
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && canonical(x) == canonical(y)
}

// newAPIAddsNewline holds when new-api's text is Axiom's with one newline more at its end.
func newAPIAddsNewline(d difference) bool {
	var a, b string
	if json.Unmarshal([]byte(d.newAPI), &a) != nil || json.Unmarshal([]byte(d.axiom), &b) != nil {
		return false
	}
	return a == b+"\n"
}

func newAPIIs(pattern string) func(difference) bool {
	re := regexp.MustCompile("^(?:" + pattern + ")$")
	return func(d difference) bool { return re.MatchString(d.newAPI) }
}

const (
	uuidText = `"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"`
	number   = `\d+`
)

// Axiom's own wording of the errors it makes, by the cases that give them (gateway.md「错误」).
var axiomWording = []struct{ cases, text string }{
	{`.*-(429|500)-.*`, "The model provider failed to complete the request."},
	{`.*-unknown-model`, "The model does not exist or is not available to this key."},
	{`.*-wrong-key`, "The API key is missing, malformed, or not valid for this address."},
	{`.*-missing-members`, "The request is not valid."},
}

// diffRules is the allowlist and the list of open differences. A rule that matches nothing in a run fails it: a
// difference that has been fixed must leave the list, or the list would one day excuse the difference coming back.
func diffRules() []*diffRule {
	rule := func(status ruleStatus, path, cases string, when func(difference) bool, reason string) *diffRule {
		r := &diffRule{path: regexp.MustCompile("^(?:" + path + ")$"), when: when, status: status, reason: reason}
		if cases != "" {
			r.cases = regexp.MustCompile("^(?:" + cases + ")$")
		}
		return r
	}
	rules := []*diffRule{
		// What is not decided yet comes first, so that a broader rule below does not take its differences.
		rule(open, `json\.error\.message`, `.*-400-.*`, axiomIs(`"The request is not valid\."`),
			"a request the upstream rejects: new-api passes the upstream's text on, Axiom says only that the request is not valid (gateway.md「待定问题」: 保留原文)"),
		rule(open, `json\.error\.message`, `.*-invalid-json`, axiomIs(`"jsontext: .*"`),
			"Axiom answers an unparsable body with the JSON reader's own message (jsontext: unexpected EOF ...), new-api with a plain sentence"),
		rule(open, `json\.error\.code`, `(chat|responses)-.*`, axiomIs(number),
			"Chat and Responses errors: OpenAI's and new-api's have a string error.code, Axiom's follow OpenRouter's with the HTTP status as error.code"),
		rule(open, `json\.error\.(param|type)`, `(chat|responses)-.*`, axiomLacks,
			"Chat and Responses errors: OpenAI's and new-api's have error.type and error.param, Axiom's do not"),
		rule(open, `json\.error\.type`, `(chat|responses)-.*`, all(newAPIIs(`"new_api_error"`), axiomIs(`"(authentication|not_found|invalid_request)_error"`)),
			"Chat and Responses errors: where Axiom has an error.type it is Anthropic's name for the failure, new-api's is new_api_error"),
		rule(open, `json\.error\.metadata\.error_type`, `(chat|responses)-.*`, newAPILacks,
			"Chat and Responses errors: Axiom puts the unified type in error.metadata.error_type (OpenRouter's place), new-api has none"),
		rule(open, `header\.x-accel-buffering`, "", values("no", absent),
			"new-api marks every event stream X-Accel-Buffering: no, which stops an nginx in front from buffering it; Axiom sends no such header"),
		rule(open, `json\.choices\[\]\.message\.reasoning_content|chat\.choices\[\]\.delta\.reasoning_content`, "", axiomLacks,
			"reasoning text in a Chat reply: new-api uses reasoning_content (DeepSeek's name, which many clients read) ..."),
		rule(open, `json\.choices\[\]\.message\.reasoning|chat\.choices\[\]\.delta\.reasoning`, "", newAPILacks,
			"... Axiom uses reasoning (OpenRouter's); the text itself is compared under reasoning_text and is the same"),
		rule(open, `sse\[response\.(created|in_progress)\]\.response\.usage`, "", values("null", absent),
			"OpenAI's Responses events carry usage: null until the response ends; Axiom leaves the member out"),

		rule(allowed, `header\.(x-oneapi-request-id|x-new-api-version)`, "", axiomLacks,
			"new-api names its request and its version; Axiom sends neither"),
		rule(allowed, `header\.(x-request-id|x-generation-id)`, "", newAPILacks,
			"Axiom's request id is the call id (gateway.md「调用编号」)"),
		rule(allowed, `header\.(cf-ray|openai-organization|x-upstream)`, "", axiomLacks,
			"new-api relays the upstream's headers; Axiom lets through only its own (gateway.md「转发规则」: 响应头按白名单放行)"),
		rule(allowed, `json\.id|chat\.id|sse\[message_start\]\.message\.id|sse\[response\.[a-z_]+\]\.response\.id`, "", axiomIs(uuidText),
			"the id of a reply is Axiom's call id, new-api passes the upstream's (gateway.md「转发规则」: 自己回显)"),
		rule(allowed, `(json|chat)\.usage\.cost(_currency|_estimated)?|sse\[message_delta\]\.usage\.cost(_currency|_estimated)?|`+
			`sse\[response\.completed\]\.response\.usage\.cost(_currency|_estimated)?`, "", newAPILacks,
			"Axiom's usage carries the call's cost (gateway.md「用量与费用」)"),
		rule(allowed, `(json|chat)\.usage\.(billing_usage\..*|usage_semantic|usage_source|claude_cache_creation_.*|input_tokens|output_tokens|`+
			`input_tokens_details|(prompt|completion)_tokens_details\.(audio|image|text)_tokens)`, `chat-.*`, axiomLacks,
			"members new-api adds to a Chat usage that are not part of the format"),
		rule(allowed, `(json|chat)\.usage\.prompt_tokens_details\.cache_write_tokens`, "", newAPILacks,
			"OpenRouter's usage reports cache writes, which Axiom's does the same way (gateway.md「用量与费用」)"),
		rule(allowed, `json\.choices\[\]\.(logprobs|message\.refusal)`, "", all(newAPILacks, axiomIs("null")),
			"members that OpenAI's reply has as null: Axiom writes them, new-api's conversion leaves them out"),
		rule(allowed, `chat\.system_fingerprint`, "", values("null", absent),
			"new-api's conversion writes a null system_fingerprint, Axiom leaves it out"),
		rule(allowed, `json\.choices\[\]\.message\.content`, `chat-gpt-(reasoning|tools)-whole`, values(`""`, "null"),
			"a message of tool calls only has content null in OpenAI's reply, new-api writes an empty string"),
		rule(allowed, `(json|chat)\.created`, "chat-.*", bothAre(number),
			"a Chat reply made from another format is stamped with the time of the call; new-api stamps the upstream's creation time or its own clock"),
		rule(allowed, `json\.choices\[\]\.message\.reasoning_details.*|chat\.choices\[\]\.delta\.reasoning_details`, "", newAPILacks,
			"OpenRouter's reasoning_details, which carry the signature and the encrypted content that a later request sends back"),
		rule(allowed, `(json|chat)\.usage\.(prompt_tokens|completion_tokens|total_tokens)`, `(chat|messages)-claude-reasoning-.*`, bothAre(number),
			"the upstream sample reports no usage, so each gateway counts the tokens itself"),
		rule(allowed, `(json|chat)\.usage\.completion_tokens_details\.reasoning_tokens`, `chat-gpt-(reasoning|tools)-.*`, values("0", "12"),
			"new-api's Responses-to-Chat conversion reports no reasoning tokens, Axiom passes the upstream's count on"),
		rule(allowed, `json\.choices\[\]\.message\.tool_calls\[\]\.function\.arguments`, `chat-claude-tools-whole`, sameJSONText,
			"Axiom builds a whole reply from the stream, so the arguments are the stream's text; new-api writes the object it was given again: the same document"),
		rule(allowed, `chat\.choices\[\]\.delta\.reasoning_text`, `chat-claude-reasoning-stream`, newAPIAddsNewline,
			"new-api ends the reasoning of a streamed thinking block with a newline"),
		rule(allowed, `sse\[ping\]`, "", values(`{"type":"ping"}`, absent),
			"nothing is sent before the first content, and a ping before it is dropped (gateway.md「保活」)"),
		rule(allowed, `sse\[message_delta\]\.usage\.input_tokens`, "", axiomLacks,
			"new-api repeats the input tokens of message_start in message_delta; Axiom sends the upstream's usage as it came"),
		rule(allowed, `header\.content-type`, "", values("application/json; charset=utf-8", "application/json"),
			"Axiom's JSON has no charset"),
		rule(allowed, `header\.content-type`, "", values("text/event-stream", "text/event-stream; charset=utf-8"),
			"Axiom's event streams say utf-8"),
		rule(allowed, `header\.content-type`, `.*-(400|429|500)-stream`, values("text/event-stream", "application/json"),
			"new-api answers a failed streaming request as an event stream around a JSON error; Axiom as a JSON error"),
		rule(allowed, `header\.cache-control`, `.*-(400|429|500)-stream`, values("no-cache", absent),
			"the same: new-api's event-stream headers on a JSON error"),
		rule(allowed, `status`, `.*-429-.*`, values("429", "502"),
			"the upstream's own 429 is never the client's: every attempt failed is 502 upstream_error (gateway.md「错误」)"),
		rule(allowed, `status`, `.*-500-.*`, values("500", "502"),
			"the upstream's own 5xx is never the client's: every attempt failed is 502 upstream_error (gateway.md「错误」)"),
		rule(allowed, `status`, `.*-unknown-model`, values("503", "404"),
			"a model that does not exist is 404 model_not_found, as OpenRouter has it; new-api answers 503 (gateway.md「决策与理由」)"),
		rule(allowed, `json\.type`, `messages-.*`, values(absent, `"error"`),
			"Anthropic's own error envelope has a type member; new-api's lacks it"),
		rule(allowed, `json\.error\.error_type`, `messages-.*`, all(newAPILacks, axiomIs(`"[a-z_]+"`)),
			"the unified error_type (gateway.md「错误」)"),
		rule(allowed, `json\.error\.type`, `messages-.*`, axiomIs(`"(invalid_request_error|authentication_error|not_found_error|api_error)"`),
			"Anthropic's error types; new-api writes its own"),
		rule(allowed, `json\.error\.code`, `messages-.*`, axiomLacks,
			"Anthropic's error has no code; new-api adds an empty one"),
	}
	for _, w := range axiomWording {
		rules = append(rules, rule(allowed, `json\.error\.message`, w.cases, axiomIs(regexp.QuoteMeta(`"`+w.text+`"`)),
			"Axiom's errors use its own unified wording (gateway.md「错误」)"))
	}
	return rules
}
