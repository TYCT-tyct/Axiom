import { expect, it } from "vitest";
import { chosenExit } from "./exit";

it.each([
  { case: "a proxy chosen", listed: true, value: "hk-1", want: "hk-1" },
  { case: "direct chosen", listed: true, value: "", want: null },
  { case: "no choice offered, with no proxy", listed: true, value: undefined, want: null },
  { case: "the proxies still to load", listed: false, value: undefined, want: undefined },
  { case: "the proxies failed to load, the key on a proxy", listed: false, value: "hk-1", want: undefined },
])("$case sends $want", ({ listed, value, want }) => {
  expect(chosenExit(listed, value)).toBe(want);
});
