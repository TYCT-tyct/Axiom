import { describe, expect, it } from "vitest";
import { check } from "./limits.ts";

const repeat = (count: number, line: string) => Array.from({ length: count }, () => line).join("\n");
const fn = (body: string) => `function f(x) {\n${body}\n}\n`;
const arrow = (body: string) => `const f = (x) => ${body};\n`;
const ors = (count: number) => `${"x || ".repeat(count)}x`;
const ifs = (count: number) => repeat(count, "  if (x > 0) x--;");

describe("check", () => {
  it.each([
    { name: "a block comment", path: "a.ts", text: fn(`  /*\n${repeat(61, "  note")}\n  */\n  return x;`), want: [] },
    { name: "JSDoc", path: "a.ts", text: fn(`  /**\n${repeat(61, "   * note")}\n   */\n  return x;`), want: [] },
    {
      name: "a JSX comment",
      path: "a.tsx",
      text: fn(
        `  return (\n    <div>\n${repeat(56, "      <br />")}\n${repeat(3, "      {/* note */}")}\n    </div>\n  );`,
      ),
      want: [],
    },
    {
      name: "code before a comment",
      path: "a.ts",
      text: fn(repeat(61, "  x++; // note")),
      want: ["a.ts:1: a function of 61 lines, more than 60"],
    },
    {
      name: "template text",
      path: "a.ts",
      text: fn(`  return \`\n${repeat(59, "// text")}\n\`;`),
      want: ["a.ts:1: a function of 61 lines, more than 60"],
    },
    {
      name: "JSX text",
      path: "a.tsx",
      text: fn(`  return (\n    <p>\n${repeat(57, "// text")}\n    </p>\n  );`),
      want: ["a.tsx:1: a function of 61 lines, more than 60"],
    },
    {
      name: "JavaScript",
      path: "a.js",
      text: fn(repeat(61, "  x++;")),
      want: ["a.js:1: a function of 61 lines, more than 60"],
    },
    { name: "a long test", path: "a.test.ts", text: fn(repeat(61, "  x++;")), want: [] },
    { name: "a long file", path: "a.js", text: repeat(501, "x++;"), want: ["a.js: 501 lines, more than 500"] },
    { name: "a file with comments", path: "a.js", text: `${repeat(500, "x++;")}\n${repeat(10, "// note")}`, want: [] },
    { name: "eleven branches", path: "a.ts", text: fn(ifs(11)), want: [] },
    // Twelve flat ifs score 12 for cognitive complexity, within Biome's 15.
    {
      name: "twelve branches",
      path: "a.ts",
      text: fn(ifs(12)),
      want: ["a.ts:1: a function of complexity 13, more than 12"],
    },
    {
      name: "every kind of branch",
      path: "a.ts",
      text: fn(`  for (;;) {}\n  for (const k in {}) {}\n  for (const v of []) {}\n  while (x) {}\n  do {} while (x);
  switch (x) { case 1: break; case 2: break; default: }\n  try {} catch {}\n  x = (x && x) || (x ?? x) ? 1 : 2;
  x &&= 1; x ||= 1; x ??= 1;`),
      want: ["a.ts:1: a function of complexity 16, more than 12"],
    },
    {
      name: "branches in a nested function",
      path: "a.ts",
      text: fn(`${ifs(6)}\n  return () => {\n${ifs(7)}\n  };`),
      want: [],
    },
    // An arrow without braces returns a single expression, which is itself a branch when it is a condition.
    { name: "an arrow returning eleven conditions", path: "a.ts", text: arrow(ors(11)), want: [] },
    {
      name: "an arrow returning twelve conditions",
      path: "a.ts",
      text: arrow(ors(12)),
      want: ["a.ts:1: a function of complexity 13, more than 12"],
    },
    {
      name: "an arrow returning twelve choices",
      path: "a.ts",
      text: arrow(`${"x ? 0 : ".repeat(12)}0`),
      want: ["a.ts:1: a function of complexity 13, more than 12"],
    },
    {
      name: "a complex test",
      path: "a.test.ts",
      text: fn(ifs(12)),
      want: ["a.test.ts:1: a function of complexity 13, more than 12"],
    },
  ])("$name", ({ path, text, want }) => {
    expect(check(path, text)).toEqual(want);
  });
});
