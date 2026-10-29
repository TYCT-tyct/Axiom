"""Write the usage samples of Claude Code session logs, for measuring billing's token estimate against them.

Each log line is an entry; a reply of the API is spread over several, one per content block, under one message ID with
its usage. A reply that did no thinking (thinking_tokens 0, which the log does not show) is an output sample: its text
and tool inputs against its output tokens. Between two replies, the growth of the context, less the first reply, is an
input sample against the tool results and messages the user's turn added; what Claude Code adds and its log leaves
out, such as reminders, lands in it as error. The samples are the user's content, so they go to a file the test reads
where it lies (internal/billing: go test -run TestEstimateError -samples <file> -v) and never into testdata.

usage: python3 scripts/usage_samples.py ~/.claude/projects/*/*.jsonl > /tmp/samples.jsonl
"""

import json
import sys


def blocks(entry):
    content = entry.get("message", {}).get("content")
    return content if isinstance(content, list) else []


def added_text(entry):
    """The text a user's entry adds to the context: a plain message, text parts and tool results."""
    content = entry["message"].get("content")
    if isinstance(content, str):
        return content
    out = []
    for b in blocks(entry):
        out.append(b.get("text", ""))
        result = b.get("content")
        if isinstance(result, str):
            out.append(result)
        elif isinstance(result, list):
            out.extend(p.get("text", "") for p in result)
    return "".join(out)


def context(usage):
    return usage["input_tokens"] + usage.get("cache_read_input_tokens", 0) + usage.get("cache_creation_input_tokens", 0)


def samples(lines):
    replies, order, added, last = {}, [], [], None
    out = []
    for line in lines:
        try:
            e = json.loads(line)
        except ValueError:
            continue
        if e.get("isSidechain"):
            continue
        if e.get("type") == "user":
            added.append(added_text(e))
            continue
        m = e.get("message", {})
        if e.get("type") != "assistant" or not m.get("usage") or not m.get("id"):
            continue
        r = replies.get(m["id"])
        if r is None:
            r = replies[m["id"]] = {"model": m.get("model", ""), "text": [], "tools": [], "thinks": False}
            order.append(r)
            text = "".join(added)
            if last is not None and not last["thinks"] and text:
                grew = context(m["usage"]) - context(last["usage"]) - last["usage"]["output_tokens"]
                if grew > 0:
                    out.append({"model": r["model"], "side": "input", "kind": "text", "text": text, "tokens": grew})
            added, last = [], r
        r["usage"] = m["usage"]
        thinking = (m["usage"].get("output_tokens_details") or {}).get("thinking_tokens")
        r["thinks"] = r["thinks"] or thinking != 0
        for b in blocks(e):
            if b.get("type") == "text":
                r["text"].append(b["text"])
            elif b.get("type") == "tool_use":
                r["tools"].append(json.dumps(b["input"], ensure_ascii=False, separators=(",", ":")))
            elif b.get("type") in ("thinking", "redacted_thinking"):
                r["thinks"] = True
    for r in order:
        text = "".join(r["text"] + r["tools"])
        if r["thinks"] or not text or r["usage"]["output_tokens"] <= 0:
            continue
        kind = "tool" if r["tools"] else "text"
        out.append({"model": r["model"], "side": "output", "kind": kind, "text": text,
                    "tokens": r["usage"]["output_tokens"]})
    return out


def main(paths):
    for path in paths:
        with open(path, encoding="utf-8") as f:
            for s in samples(f):
                print(json.dumps(s, ensure_ascii=False))


if __name__ == "__main__":
    main(sys.argv[1:])
