import json
import unittest

import usage_samples


def usage(context, output, thinking=0):
    return {"input_tokens": 1, "cache_read_input_tokens": context - 1, "output_tokens": output,
            "output_tokens_details": {"thinking_tokens": thinking}}


def reply(mid, block, u):
    return json.dumps({"type": "assistant", "message": {"id": mid, "model": "claude-opus-5-5", "content": [block],
                                                        "usage": u}})


def result(text):
    return json.dumps({"type": "user", "message": {"content": [
        {"type": "tool_result", "content": [{"type": "text", "text": text}]}]}})


class Samples(unittest.TestCase):
    def test_a_reply_without_thinking_is_an_output_sample_and_the_growth_an_input_one(self):
        lines = [
            reply("a", {"type": "text", "text": "Listing."}, usage(100, 30)),
            reply("a", {"type": "tool_use", "input": {"command": "ls"}}, usage(100, 30)),
            result("go.mod\nmain.go"),
            reply("b", {"type": "text", "text": "Done."}, usage(140, 3)),
        ]
        got = usage_samples.samples(lines)
        self.assertIn({"model": "claude-opus-5-5", "side": "input", "kind": "text", "text": "go.mod\nmain.go",
                       "tokens": 10}, got)
        self.assertIn({"model": "claude-opus-5-5", "side": "output", "kind": "tool",
                       "text": 'Listing.{"command":"ls"}', "tokens": 30}, got)
        self.assertIn({"model": "claude-opus-5-5", "side": "output", "kind": "text", "text": "Done.", "tokens": 3}, got)

    def test_thinking_hides_part_of_the_output_and_of_the_next_context(self):
        lines = [
            reply("a", {"type": "thinking", "thinking": ""}, usage(100, 30, thinking=20)),
            reply("a", {"type": "text", "text": "Hi"}, usage(100, 30, thinking=20)),
            result("x"),
            reply("b", {"type": "text", "text": "Ok"}, usage(140, 3)),
        ]
        self.assertEqual([s["text"] for s in usage_samples.samples(lines)], ["Ok"])

    def test_side_chains_and_broken_lines_are_skipped(self):
        side = json.loads(reply("a", {"type": "text", "text": "Hi"}, usage(100, 3)))
        side["isSidechain"] = True
        self.assertEqual(usage_samples.samples([json.dumps(side), "{"]), [])


if __name__ == "__main__":
    unittest.main()
