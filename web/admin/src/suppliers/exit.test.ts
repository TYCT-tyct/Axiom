import { expect, it } from "vitest";
import { chosenExit, egressRead } from "./exit";

it.each([
  { case: "a proxy chosen", listed: true, value: "hk-1", want: "hk-1" },
  { case: "direct chosen", listed: true, value: "", want: null },
  { case: "no choice offered, with no proxy", listed: true, value: undefined, want: null },
  { case: "the proxies still to load", listed: false, value: undefined, want: undefined },
  { case: "the proxies failed to load, the key on a proxy", listed: false, value: "hk-1", want: undefined },
])("$case sends $want", ({ listed, value, want }) => {
  expect(chosenExit(listed, value)).toBe(want);
});

it.each([
  { case: "direct chosen after the list loaded", listed: true, value: "", want: null },
  { case: "a proxy chosen", listed: true, value: "hk-1", want: "hk-1" },
  { case: "the list not loaded", listed: false, value: "hk-1", want: { problem: "wait" } },
])("the form reads $case as $want", ({ listed, value, want }) => {
  expect(egressRead(listed, value, "wait")).toEqual(want);
});
