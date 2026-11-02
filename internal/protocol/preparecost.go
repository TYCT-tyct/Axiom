package protocol

// PrepareCost is the most memory a same-format rewrite holds at once, besides the body itself: the buffer it writes
// into, whose capacity is known before it starts, and the scratch an identifier or a response ID is unquoted into,
// which the body's size bounds three times over. A conversion between formats is not bounded here: PrepareBudgeted
// reads it with a budget and charges what it allocates as it allocates it.
func PrepareCost(r *Request, body []byte, t Target, w Rewrites) int64 {
	if r.Format != t.Format || !r.Format.Convertible() {
		return 0
	}
	return rewriteCost(r, body, t, w)
}

// rewriteCost is what a same-format rewrite holds at once: the buffer it writes into, the scratch an identifier or a
// response ID is unquoted into and the string it becomes, and a legacy request's conversion to the canonical model.
func rewriteCost(r *Request, body []byte, t Target, w Rewrites) int64 {
	n := int64(len(body)) + nativeSearchCost(w.NativeSearch) + serverFunctionCost(w.ServerFunction)
	cost := int64(rewriteSize(rulesFor(r.Format, t), len(body), t, w)) + 3*n + replayCost(r.Format, body)
	if r.Format == Responses {
		cost += replayHistoryCost(w.History)
	}
	if r.Format == Chat && r.legacy {
		cost += nativeRequestCost(body)
	}
	return cost
}

// nativeSearchCost bounds what adding the target format's web search tool costs: the tool object and its member name.
// It is charged before the body is prepared, so the estimate includes it even though the tool is written afterwards.
func nativeSearchCost(uses int) int64 {
	if uses <= 0 {
		return 0
	}
	return 256
}

// serverFunctionCost is the room the web search function declaration takes, escaped and with its schema.
func serverFunctionCost(add bool) int64 {
	if !add {
		return 0
	}
	return 4 << 10
}
