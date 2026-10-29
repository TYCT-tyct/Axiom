//go:build diff

package main

import (
	"encoding/json/v2" //nolint:depguard // reads what the two gateways answered; jsonx guards input from outside
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// A response is compared as a set of leaves, each a scalar named by its path: the status, the headers, and the body,
// which is a JSON document, a stream of server-sent events, or text. A leaf has a path without positions
// ("json.choices[].message.content"), which the rules of diff_rules_test.go match, and a place with them
// ("json.choices[0].message.content"), which pairs the leaf of one response with the leaf of the other. JSON objects
// are compared by member, so the order of the members does not matter; arrays are compared by position.
type leaf struct{ path, at, value string }

// absent is the value of a leaf the other response has and this one does not.
const absent = "<absent>"

// difference is a leaf the two responses do not share.
type difference struct{ path, at, newAPI, axiom string }

// framing headers are not part of what a gateway answers: the server writes them for the connection.
var framingHeaders = []string{"Date", "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive"}

// leavesOf reads a response into its leaves.
func leavesOf(status int, header http.Header, body []byte) []leaf {
	out := []leaf{{"status", "status", strconv.Itoa(status)}}
	for name, values := range header {
		if !slices.Contains(framingHeaders, http.CanonicalHeaderKey(name)) {
			path := "header." + strings.ToLower(name)
			out = append(out, leaf{path, path, strings.Join(values, ", ")})
		}
	}
	return append(out, bodyLeaves(header.Get("Content-Type"), string(body))...)
}

func bodyLeaves(contentType, body string) []leaf {
	var doc any
	switch {
	case strings.HasPrefix(contentType, "text/event-stream"):
		return nil // compared as events, by compareEvents
	case json.Unmarshal([]byte(body), &doc) == nil:
		return flatten("json", "json", doc)
	default:
		return []leaf{{"text", "text", body}}
	}
}

// flatten lists the scalars of a decoded JSON value; an empty object or array is a scalar.
func flatten(path, at string, v any) []leaf {
	switch x := v.(type) {
	case map[string]any:
		if len(x) > 0 {
			var out []leaf
			for k, e := range x {
				out = append(out, flatten(path+"."+k, at+"."+k, e)...)
			}
			return out
		}
		return []leaf{{path, at, "{}"}}
	case []any:
		if len(x) > 0 {
			var out []leaf
			for i, e := range x {
				out = append(out, flatten(path+"[]", at+"["+strconv.Itoa(i)+"]", e)...)
			}
			return out
		}
		return []leaf{{path, at, "[]"}}
	default:
		return []leaf{{path, at, canonical(x)}}
	}
}

// canonical is a value's one JSON text: members sorted, numbers as the shortest text that reads back the same.
func canonical(v any) string {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return "unmarshalable: " + err.Error()
	}
	return string(b)
}

// compareLeaves lists the leaves the two share neither in place nor in value, by place.
func compareLeaves(newAPI, axiom []leaf) []difference {
	at := func(leaves []leaf) map[string]leaf {
		m := make(map[string]leaf, len(leaves))
		for _, l := range leaves {
			m[l.at] = l
		}
		return m
	}
	n, a := at(newAPI), at(axiom)
	var out []difference
	for place, l := range n {
		if other, ok := a[place]; !ok {
			out = append(out, difference{l.path, place, l.value, absent})
		} else if other.value != l.value {
			out = append(out, difference{l.path, place, l.value, other.value})
		}
	}
	for place, l := range a {
		if _, ok := n[place]; !ok {
			out = append(out, difference{l.path, place, absent, l.value})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// event is one server-sent event: its name ("data" when it has none) and its data, canonical when it is JSON.
type event struct {
	name, data string
	doc        any // the decoded data, nil when it is not JSON
}

var eventLine = regexp.MustCompile(`^(event|data): ?(.*)$`)

// parseEvents reads a stream. A block of comment lines only (": ping") is not an event.
func parseEvents(body string) []event {
	var out []event
	for block := range strings.SplitSeq(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		name, data, found := "data", []string(nil), false
		for line := range strings.SplitSeq(block, "\n") {
			m := eventLine.FindStringSubmatch(line)
			switch {
			case m == nil:
			case m[1] == "event":
				name = m[2]
			default:
				data, found = append(data, m[2]), true
			}
		}
		if found {
			out = append(out, readEvent(name, strings.Join(data, "\n")))
		}
	}
	return out
}

func readEvent(name, data string) event {
	var doc any
	if json.Unmarshal([]byte(data), &doc) != nil {
		return event{name: name, data: data}
	}
	return event{name: name, data: canonical(doc), doc: doc}
}

// compareEvents lines the two streams up as a person would diff them: events that are the same in both anchor it, and
// between anchors an event of one is paired with the next event of the other with the same name, which is compared by
// leaf; what is left over is an event only one stream has.
func compareEvents(newAPI, axiom []event) []difference {
	var out []difference
	if theirs, ours, differ := eventOrder(newAPI, axiom); differ {
		out = append(out, difference{"sse.order", "sse.order", theirs, ours})
	}
	if theirs, ours := stopIndexes(newAPI), stopIndexes(axiom); theirs != ours {
		out = append(out, difference{"sse[content_block_stop].indexes", "sse[content_block_stop].indexes", theirs, ours})
	}
	i, j := 0, 0
	for _, m := range anchors(newAPI, axiom) {
		out = append(out, compareGap(newAPI[i:m[0]], axiom[j:m[1]], i, j)...)
		i, j = m[0]+1, m[1]+1
	}
	return append(out, compareGap(newAPI[i:], axiom[j:], i, j)...)
}

// eventOrder is the event names one stream sent, grouped by what they are about: each Responses output item or Messages
// content block keeps its own sequence, and events without an index keep theirs, apart from the opening and closing
// events, which must stay on their side of the stream. The pairing above matches by name, so two differently named
// events swapped within one group still pair with the right partners and compare equal leaf by leaf; sequence numbers
// travel with their events and cannot catch that either. Comparing the groups catches it, while groups let independent
// items and blocks close in a different order, as the protocols allow.
//
// A name whose count differs between the streams is left out of the comparison: it is an event one stream has and the
// other does not, which the pairing already reports. A name both streams have the same number of times must appear in
// the same groups and the same order within each.
func eventOrder(newAPI, axiom []event) (theirs, ours string, differ bool) {
	t, a := orderOf(newAPI), orderOf(axiom)
	keys := make([]string, 0, len(t.groups)+len(a.groups))
	for key := range t.groups {
		keys = append(keys, key)
	}
	for key := range a.groups {
		if _, ok := t.groups[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	tParts, aParts := make([]string, 0, len(keys)), make([]string, 0, len(keys))
	for _, key := range keys {
		tParts = append(tParts, key+"="+strings.Join(commonOrder(t.groups[key], t.counts, a.counts), ","))
		aParts = append(aParts, key+"="+strings.Join(commonOrder(a.groups[key], t.counts, a.counts), ","))
	}
	theirs = strings.Join(tParts, ";") + "|" + placementOrder(t, a.counts)
	ours = strings.Join(aParts, ";") + "|" + placementOrder(a, t.counts)
	return theirs, ours, theirs != ours
}

// orderView is one stream's groups of event names, how many times each name occurs in the whole stream, and whether
// each opening or closing event is on its side of the indexed events.
type orderView struct {
	groups map[string][]string
	counts map[string]int
	placed map[string]map[bool]int
}

// orderOf groups a stream's events. Indexed events go by their index. A stream's opening events must be before its
// first indexed event and its closing events after its last; any other unindexed event is kept in one sequence, so a
// content block that closes later does not move it. Ping is a keepalive and carries no order.
func orderOf(events []event) orderView {
	index := make([]string, len(events))
	indexed := make([]bool, len(events))
	first, last := -1, -1
	for i, e := range events {
		if idx, ok := eventIndex(e); ok {
			index[i], indexed[i] = idx, true
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	o := orderView{groups: map[string][]string{}, counts: map[string]int{}, placed: map[string]map[bool]int{}}
	for i, e := range events {
		if e.name == "ping" {
			continue
		}
		group := "u"
		if indexed[i] {
			group = "i:" + index[i]
		}
		o.groups[group] = append(o.groups[group], e.name)
		o.counts[e.name]++
		switch {
		case isOpeningEvent(e.name):
			o.place(e.name, first >= 0 && i < first)
		case isClosingEvent(e.name):
			o.place(e.name, last >= 0 && i > last)
		}
	}
	return o
}

func (o orderView) place(name string, inPlace bool) {
	if o.placed[name] == nil {
		o.placed[name] = map[bool]int{}
	}
	o.placed[name][inPlace]++
}

// placementOrder is where the opening and closing events both streams have the same number of times sit: before the
// first indexed event and after the last, or not. A stream that moved its lifecycle around its items is caught here
// even when its unindexed event order is unchanged.
func placementOrder(v orderView, other map[string]int) string {
	parts := make([]string, 0, len(v.placed))
	for name, places := range v.placed {
		if v.counts[name] == other[name] {
			parts = append(parts, name+":"+strconv.Itoa(places[true])+"/"+strconv.Itoa(places[false]))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// commonOrder keeps the names both streams have the same number of times, in this stream's order.
func commonOrder(names []string, theirs, ours map[string]int) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if theirs[name] == ours[name] {
			out = append(out, name)
		}
	}
	return out
}

// eventIndex is the item or block an event belongs to: output_index for Responses, index for Messages.
func eventIndex(e event) (string, bool) {
	doc, ok := e.doc.(map[string]any)
	if !ok {
		return "", false
	}
	member := "index"
	if strings.HasPrefix(e.name, "response.") {
		member = "output_index"
	}
	v, ok := doc[member]
	if !ok {
		return "", false
	}
	return canonical(v), true
}

func isOpeningEvent(name string) bool {
	return name == "message_start" || name == "response.created" || name == "response.in_progress"
}

func isClosingEvent(name string) bool {
	switch name {
	case "message_stop", "response.completed", "response.failed", "response.incomplete":
		return true
	}
	return false
}

// A moved stop may be paired as two unmatched events, but a lost or duplicated block must still fail the comparison.
func stopIndexes(events []event) string {
	var indexes []string
	for _, e := range events {
		if e.name == "content_block_stop" {
			if doc, ok := e.doc.(map[string]any); ok {
				indexes = append(indexes, canonical(doc["index"]))
			}
		}
	}
	slices.Sort(indexes)
	return canonical(indexes)
}

// anchors is the longest run of events both streams have, in order, as pairs of positions.
func anchors(a, b []event) [][2]int {
	same := func(x, y event) bool { return x.name == y.name && x.data == y.data }
	t := make([][]int, len(a)+1)
	for i := range t {
		t[i] = make([]int, len(b)+1)
	}
	for i := range slices.Backward(a) {
		for j := range slices.Backward(b) {
			switch {
			case same(a[i], b[j]):
				t[i][j] = t[i+1][j+1] + 1
			default:
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	var out [][2]int
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case same(a[i], b[j]):
			out = append(out, [2]int{i, j})
			i, j = i+1, j+1
		case t[i+1][j] >= t[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

// compareGap compares the events between two anchors; offA and offB are where the gap starts in each stream.
func compareGap(a, b []event, offA, offB int) []difference {
	var out []difference
	paired := make([]bool, len(b))
	for i, x := range a {
		j := -1
		var partner event
		for k, y := range b {
			if !paired[k] && sameEventItem(x, y) {
				j, partner = k, y
				break
			}
		}
		path := "sse[" + x.name + "]"
		if j < 0 {
			out = append(out, difference{path, path + "#" + strconv.Itoa(offA+i) + "/-", x.data, absent})
			continue
		}
		paired[j] = true
		out = append(out, compareLeaves(eventLeaves(path, x, offA+i, offB+j), eventLeaves(path, partner, offA+i, offB+j))...)
	}
	for j, y := range b {
		if !paired[j] {
			path := "sse[" + y.name + "]"
			out = append(out, difference{path, path + "#-/" + strconv.Itoa(offB+j), absent, y.data})
		}
	}
	return out
}

// Responses may close concurrent items in a different order; compare the same output item, retaining its event position.
func sameEventItem(a, b event) bool {
	if a.name != b.name {
		return false
	}
	x, xok := a.doc.(map[string]any)
	y, yok := b.doc.(map[string]any)
	return !strings.HasPrefix(a.name, "response.") || !xok || !yok || x["output_index"] == y["output_index"]
}

// eventLeaves is the leaves of an event's data, placed by the pair the event belongs to, so its partner's are placed alike.
func eventLeaves(path string, e event, i, j int) []leaf {
	at := path + "#" + strconv.Itoa(i) + "/" + strconv.Itoa(j)
	if e.doc == nil {
		return []leaf{{path, at, e.data}}
	}
	return flatten(path, at, e.doc)
}

// compareReplies lists everything the two gateways answered differently, new-api's answer first.
func compareReplies(newAPI, axiom reply) []difference {
	out := compareLeaves(leavesOf(newAPI.status, newAPI.header, newAPI.body), leavesOf(axiom.status, axiom.header, axiom.body))
	theirs, ours := parseEvents(string(newAPI.body)), parseEvents(string(axiom.body))
	if isChatStream(theirs) || isChatStream(ours) {
		return append(out, compareLeaves(foldChat(theirs), foldChat(ours))...)
	}
	return append(out, compareEvents(theirs, ours)...)
}

// A Chat stream is compared by what it adds up to, not chunk by chunk: where the chunks are cut is a converter's own
// choice, and a client reads the text, the tool calls, the finish reason and the usage the chunks come to. The
// message ids and times are leaves too, as the first chunk gives them.
func isChatStream(events []event) bool {
	if len(events) == 0 {
		return false
	}
	doc, ok := events[0].doc.(map[string]any)
	return ok && doc["object"] == "chat.completion.chunk"
}

// foldChat adds a Chat stream up: the members of every chunk's delta, their strings decoded and joined in order, the
// tool calls joined by index, the last finish reason and the last usage, and how the stream ends: exactly one [DONE],
// after which nothing comes. A client stops reading at the first [DONE], so chunks after it are lost to it and are
// counted, not folded; any other data that is not JSON is kept as it is.
func foldChat(events []event) []leaf {
	f := chatFold{texts: map[leafKey]string{}, sets: map[leafKey]string{}}
	done := slices.IndexFunc(events, func(e event) bool { return e.doc == nil && e.data == "[DONE]" })
	body := events
	if done >= 0 {
		body = events[:done]
	}
	count := 0
	for i, e := range events {
		if e.doc == nil && e.data == "[DONE]" {
			count++
		} else if e.doc == nil {
			f.sets[leafKey{"chat.stray", "chat.stray[" + strconv.Itoa(i) + "]"}] = canonical(e.data)
		}
	}
	f.sets[leafKey{"chat.done.count", "chat.done.count"}] = strconv.Itoa(count)
	f.sets[leafKey{"chat.done.after", "chat.done.after"}] = strconv.Itoa(max(0, len(events)-done-1) * min(1, count))
	for _, e := range body {
		if chunk, ok := e.doc.(map[string]any); ok {
			f.chunk(chunk)
		}
	}
	return f.leaves()
}

// leafKey is where a folded value lives: its path and its place.
type leafKey struct{ path, at string }

// chatFold is what the chunks of a Chat stream add up to: strings that are joined as decoded text, and values that
// are set (the last chunk's wins).
type chatFold struct {
	texts, sets map[leafKey]string
	usage       []leaf
}

func (f *chatFold) leaves() []leaf {
	out := f.usage
	for k, v := range f.sets {
		out = append(out, leaf{k.path, k.at, v})
	}
	for k, v := range f.texts {
		out = append(out, leaf{k.path, k.at, canonical(v)})
	}
	return out
}

func (f *chatFold) chunk(chunk map[string]any) {
	for k, v := range chunk {
		switch k {
		case "choices":
			f.choices(v)
		case "usage":
			if v != nil {
				f.usage = flatten("chat.usage", "chat.usage", v)
			}
		default:
			f.sets[leafKey{"chat." + k, "chat." + k}] = canonical(v)
		}
	}
}

func (f *chatFold) choices(choices any) {
	list, _ := choices.([]any)
	for _, c := range list {
		choice, _ := c.(map[string]any)
		at := "chat.choices[" + indexOf(choice) + "]"
		if reason, ok := choice["finish_reason"].(string); ok {
			f.sets[leafKey{"chat.choices[].finish_reason", at + ".finish_reason"}] = canonical(reason)
		}
		delta, _ := choice["delta"].(map[string]any)
		for k, v := range delta {
			switch x := v.(type) {
			case string:
				f.text(leafKey{"chat.choices[].delta." + k, at + ".delta." + k}, x)
			case []any:
				f.list(at+".delta."+k, "chat.choices[].delta."+k, k, x)
			}
		}
	}
}

func (f *chatFold) text(k leafKey, piece string) { f.texts[k] += piece }

// list joins the elements of a delta's list: a tool call is added up with the others of its index, with its arguments
// decoded piece by piece and joined, the rest are listed in order.
func (f *chatFold) list(at, path, key string, list []any) {
	for _, item := range list {
		call, ok := item.(map[string]any)
		if !ok || key != "tool_calls" {
			f.text(leafKey{path, at}, canonical(item))
			continue
		}
		callAt := at + "[" + indexOf(call) + "]"
		for k, v := range call {
			if fn, isFn := v.(map[string]any); isFn && k == "function" {
				f.function(callAt+".function", path+"[].function", fn)
			} else if k != "index" {
				f.sets[leafKey{path + "[]." + k, callAt + "." + k}] = canonical(v)
			}
		}
	}
}

func (f *chatFold) function(at, path string, fn map[string]any) {
	for k, v := range fn {
		if arguments, ok := v.(string); ok && k == "arguments" {
			f.text(leafKey{path + ".arguments", at + ".arguments"}, arguments)
		} else {
			f.sets[leafKey{path + "." + k, at + "." + k}] = canonical(v)
		}
	}
}

// indexOf is the "index" member of a choice or a tool call, 0 when it has none.
func indexOf(m map[string]any) string {
	n, _ := m["index"].(float64)
	return strconv.Itoa(int(n))
}

// reasoningNamesAgree checks what the reasoning_content rule leaves to Axiom (diff_rules_test.go): the reasoning of a Chat
// reply is written under two names, and a client that reads only reasoning must get the text a client that reads only
// reasoning_content gets. In a stream every delta is held to it, in a whole reply every message.
func reasoningNamesAgree(r reply) error {
	if strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		for i, e := range parseEvents(string(r.body)) {
			chunk, _ := e.doc.(map[string]any)
			choices, _ := chunk["choices"].([]any)
			for _, c := range choices {
				choice, _ := c.(map[string]any)
				if err := namesAgree(choice["delta"]); err != nil {
					return fmt.Errorf("event %d: %w", i, err)
				}
			}
		}
		return nil
	}
	var doc struct {
		Choices []struct {
			Message any `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(r.body, &doc); err != nil {
		return fmt.Errorf("a reply that is neither a stream nor JSON: %w", err)
	}
	for i, c := range doc.Choices {
		if err := namesAgree(c.Message); err != nil {
			return fmt.Errorf("choice %d: %w", i, err)
		}
	}
	return nil
}

// namesAgree holds when a message or delta has reasoning and reasoning_content with the same text, or neither.
func namesAgree(v any) error {
	m, _ := v.(map[string]any)
	a, _ := m["reasoning"].(string)
	b, _ := m["reasoning_content"].(string)
	if a != b {
		return fmt.Errorf("reasoning %q but reasoning_content %q", a, b)
	}
	return nil
}
