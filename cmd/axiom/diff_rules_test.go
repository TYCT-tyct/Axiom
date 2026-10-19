//go:build diff

package main

import (
	"encoding/json/v2" //nolint:depguard // reads values of replies; jsonx guards input from outside
	"regexp"
)

// diffRule explains the differences whose path matches, in the cases whose name matches, and whose two values are the
// ones the reason is about: a rule is for one difference, not for whatever else happens at the same path.
type diffRule struct {
	path, cases *regexp.Regexp
	when        func(difference) bool
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

// missingMembersSaid are the values Axiom answers a request without its conversation with: the code, param and message of
// the fake upstream's canned 400, which is the supplier's word on it (gateway.md「错误」).
const missingMembersSaid = `null|"invalid_value"|"max_tokens"|"max_tokens: Input should be greater than 0"|` +
	`"Invalid value for 'max_tokens': expected a value above 0\."`

// Axiom's own wording of the errors it makes, by the cases that give them (gateway.md「错误」).
var axiomWording = []struct{ cases, text string }{
	{`.*-(429|500)-.*`, "The model provider failed to complete the request."},
	{`.*-unknown-model`, "The model does not exist or is not available to this key."},
	{`.*-wrong-key`, "The API key is missing, malformed, or not valid for this address."},
}

// diffRules is the allowlist, every rule a difference Axiom has on purpose (docs/domains/gateway.md「与 new-api 的有意差异」).
// A rule that matches nothing in a run fails it: a difference that has been fixed must leave the list, or the list would
// one day excuse the difference coming back. A difference nobody has ruled on yet is not listed: it fails the run until
// its issue decides it.
func diffRules() []*diffRule {
	rule := func(path, cases string, when func(difference) bool, reason string) *diffRule {
		r := &diffRule{path: regexp.MustCompile("^(?:" + path + ")$"), when: when, reason: reason}
		if cases != "" {
			r.cases = regexp.MustCompile("^(?:" + cases + ")$")
		}
		return r
	}
	rules := []*diffRule{
		rule(`header\.x-accel-buffering`, `.*-(400|429|500)-stream`, values("no", absent),
			"new-api marks the event stream it answers a failed streaming request with; Axiom fails before any stream starts, with a JSON error (gateway.md「转发规则」)"),
		rule(`json\.error\..*`, `.*-(400|429|500)-stream`, newAPILacks,
			"new-api answers a failed streaming request as an event stream, whose events are not compared here; Axiom's JSON error has these members"),
		rule(`json\.error\.metadata\.error_type`, `(chat|responses)-.*`, all(newAPILacks, axiomIs(`"[a-z_]+"`)),
			"the unified error_type (gateway.md「错误」), which new-api has no counterpart for"),
		rule(`json\.error\.type`, `(chat|responses)-.*-(invalid-json|missing-members|unknown-model|wrong-key)`,
			all(newAPIIs(`"new_api_error"`), axiomIs(`"(invalid_request|authentication|not_found)_error"`)),
			"new-api puts its own name, new_api_error, in the type of the errors it makes; Axiom's is the type of the format's clients, as Messages' (gateway.md「错误」)"),
		rule(`json\.error\.code`, `(chat|responses)-.*-(invalid-json|wrong-key)`, all(newAPIIs(`""`), axiomIs(`"(invalid_request|authentication)"`)),
			"new-api leaves the code empty for a body that is not JSON and for a wrong key; Axiom's is the unified error type"),
		rule(`json\.error\.param`, `(chat|responses)-.*-(invalid-json|unknown-model|wrong-key)`, values(absent, `""`),
			"the errors new-api's middleware makes (a body that is not JSON, an unknown model, a wrong key) have no param member; Axiom's errors all have one"),
		rule(`json\.error\.(type|code)`, `(chat|responses)-.*-(429|500)-whole`, axiomIs(`"(api_error|upstream_error)"`),
			"an upstream's 429 or 5xx is Axiom's own 502 upstream_error, so its type and code are Axiom's; new-api passes the upstream's on"),
		rule(`json\.error\.(code|param|message)`, `(chat|responses|messages)-.*-missing-members`, axiomIs(missingMembersSaid),
			"Axiom does not check that a request has its conversation: the supplier refuses it and the client reads the supplier's words (here the fake upstream's canned 400); new-api checks first and answers with its own"),
		rule(`json\.choices\[\]\.message\.reasoning|chat\.choices\[\]\.delta\.reasoning`, "", newAPILacks,
			"Axiom writes reasoning, OpenRouter's name, beside reasoning_content, new-api's and DeepSeek's, with the same text; the text itself is compared under reasoning_text (gateway.md「转发规则」)"),

		rule(`header\.(x-oneapi-request-id|x-new-api-version)`, "", axiomLacks,
			"new-api names its request and its version; Axiom sends neither"),
		rule(`header\.(x-request-id|x-generation-id)`, "", newAPILacks,
			"Axiom's request id is the call id (gateway.md「调用编号」)"),
		rule(`header\.(cf-ray|openai-organization|x-upstream)`, "", axiomLacks,
			"new-api relays the upstream's headers; Axiom lets through only its own (gateway.md「转发规则」: 响应头按白名单放行)"),
		rule(`json\.id|chat\.id|sse\[message_start\]\.message\.id|sse\[response\.[a-z_]+\]\.response\.id`, "", axiomIs(uuidText),
			"the id of a reply is Axiom's call id, new-api passes the upstream's (gateway.md「转发规则」: 自己回显)"),
		rule(`(json|chat)\.usage\.cost(_currency|_estimated)?|sse\[message_delta\]\.usage\.cost(_currency|_estimated)?|`+
			`sse\[response\.completed\]\.response\.usage\.cost(_currency|_estimated)?`, "", newAPILacks,
			"Axiom's usage carries the call's cost (gateway.md「用量与费用」)"),
		rule(`(json|chat)\.usage\.(billing_usage\..*|usage_semantic|usage_source|claude_cache_creation_.*|input_tokens|output_tokens|`+
			`input_tokens_details|(prompt|completion)_tokens_details\.(audio|image|text)_tokens)`, `chat-.*`, axiomLacks,
			"members new-api adds to a Chat usage that are not part of the format"),
		rule(`(json|chat)\.usage\.prompt_tokens_details\.cache_write_tokens`, "", newAPILacks,
			"OpenRouter's usage reports cache writes, which Axiom's does the same way (gateway.md「用量与费用」)"),
		rule(`json\.choices\[\]\.(logprobs|message\.refusal)`, "", all(newAPILacks, axiomIs("null")),
			"members that OpenAI's reply has as null: Axiom writes them, new-api's conversion leaves them out"),
		rule(`chat\.system_fingerprint`, "", values("null", absent),
			"new-api's conversion writes a null system_fingerprint, Axiom leaves it out"),
		rule(`json\.choices\[\]\.message\.content`, `chat-gpt-(reasoning|tools)-whole`, values(`""`, "null"),
			"a message of tool calls only has content null in OpenAI's reply, new-api writes an empty string"),
		rule(`(json|chat)\.created`, "chat-.*", bothAre(number),
			"a Chat reply made from another format is stamped with the time of the call; new-api stamps the upstream's creation time or its own clock"),
		rule(`json\.choices\[\]\.message\.reasoning_details.*|chat\.choices\[\]\.delta\.reasoning_details`, "", newAPILacks,
			"OpenRouter's reasoning_details, which carry the signature and the encrypted content that a later request sends back"),
		rule(`(json|chat)\.usage\.(prompt_tokens|completion_tokens|total_tokens)`, `(chat|messages)-claude-reasoning-.*`, bothAre(number),
			"the upstream sample reports no usage, so each gateway counts the tokens itself"),
		rule(`(json|chat)\.usage\.completion_tokens_details\.reasoning_tokens`, `chat-gpt-(reasoning|tools)-.*`, values("0", "12"),
			"new-api's Responses-to-Chat conversion reports no reasoning tokens, Axiom passes the upstream's count on"),
		rule(`json\.choices\[\]\.message\.tool_calls\[\]\.function\.arguments`, `chat-claude-tools-whole`, sameJSONText,
			"Axiom builds a whole reply from the stream, so the arguments are the stream's text; new-api writes the object it was given again: the same document"),
		rule(`chat\.choices\[\]\.delta\.reasoning_(text|content)`, `chat-claude-reasoning-stream`, newAPIAddsNewline,
			"new-api ends the reasoning of a streamed thinking block with a newline"),
		rule(`sse\[ping\]`, "", values(`{"type":"ping"}`, absent),
			"nothing is sent before the first content, and a ping before it is dropped (gateway.md「保活」)"),
		rule(`sse\[message_delta\]\.usage\.input_tokens`, "", axiomLacks,
			"new-api repeats the input tokens of message_start in message_delta; Axiom sends the upstream's usage as it came"),
		rule(`header\.content-type`, "", values("application/json; charset=utf-8", "application/json"),
			"Axiom's JSON has no charset"),
		rule(`header\.content-type`, "", values("text/event-stream", "text/event-stream; charset=utf-8"),
			"Axiom's event streams say utf-8"),
		rule(`header\.content-type`, `.*-(400|429|500)-stream`, values("text/event-stream", "application/json"),
			"new-api answers a failed streaming request as an event stream around a JSON error; Axiom as a JSON error"),
		rule(`header\.cache-control`, `.*-(400|429|500)-stream`, values("no-cache", absent),
			"the same: new-api's event-stream headers on a JSON error"),
		rule(`status`, `.*-429-.*`, values("429", "502"),
			"the upstream's own 429 is never the client's: every attempt failed is 502 upstream_error (gateway.md「错误」)"),
		rule(`status`, `.*-500-.*`, values("500", "502"),
			"the upstream's own 5xx is never the client's: every attempt failed is 502 upstream_error (gateway.md「错误」)"),
		rule(`status`, `.*-unknown-model`, values("503", "404"),
			"a model that does not exist is 404 model_not_found, as OpenRouter has it; new-api answers 503 (gateway.md「决策与理由」)"),
		rule(`json\.type`, `messages-.*`, values(absent, `"error"`),
			"Anthropic's own error envelope has a type member; new-api's lacks it"),
		rule(`json\.error\.error_type`, `messages-.*`, all(newAPILacks, axiomIs(`"[a-z_]+"`)),
			"the unified error_type (gateway.md「错误」)"),
		rule(`json\.error\.type`, `messages-.*`, axiomIs(`"(invalid_request_error|authentication_error|not_found_error|api_error)"`),
			"Anthropic's error types; new-api writes its own"),
		rule(`json\.error\.code`, `messages-.*`, axiomLacks,
			"Anthropic's error has no code; new-api adds an empty one"),
	}
	for _, w := range axiomWording {
		rules = append(rules, rule(`json\.error\.message`, w.cases, axiomIs(regexp.QuoteMeta(`"`+w.text+`"`)),
			"Axiom's errors use its own unified wording (gateway.md「错误」)"))
	}
	return rules
}
