import { expect, it } from "vitest";
import { formatJSON, parseJSONLossless } from "./lossless";

it("keeps a number that JavaScript would round as it was written", () => {
  const parsed = parseJSONLossless(
    '{"before":9007199254740993,"nested":[12345678901234567890],"ratio":0.5,"whole":1.0}',
  );
  const shown = formatJSON(parsed);
  expect(shown).toContain("9007199254740993");
  expect(shown).toContain("12345678901234567890");
  expect(shown).toContain("0.5");
  expect(shown).toContain("1.0");
  expect(shown).not.toContain("9007199254740992");
});

it("keeps strings, booleans, null and ordinary integers as they were", () => {
  const parsed = parseJSONLossless('{"name":"a\\"b","ok":true,"none":null,"count":42,"items":[]}');
  expect(formatJSON(parsed)).toBe(
    '{\n  "name": "a\\"b",\n  "ok": true,\n  "none": null,\n  "count": 42,\n  "items": []\n}',
  );
});

it("keeps a __proto__ member as data, object or scalar", () => {
  const object = parseJSONLossless('{"__proto__":{"before":9007199254740993},"after":1}');
  expect(Object.keys(object as object)).toEqual(["__proto__", "after"]);
  const shown = formatJSON(object);
  expect(shown).toContain('"__proto__"');
  expect(shown).toContain('"before": 9007199254740993');

  const scalar = parseJSONLossless('{"__proto__":"kept","next":2}');
  expect(formatJSON(scalar)).toContain('"__proto__": "kept"');
});
