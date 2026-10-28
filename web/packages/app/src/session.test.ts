import { expect, it } from "vitest";
import { safeNext } from "./session";

const dots = [
  // the issue's case: the router decodes %2F, and the browser resolves `..` out of the base
  { value: "/../outside", want: undefined },
  { value: "/%2e%2e/outside", want: undefined },
  { value: "/a/../../outside", want: undefined },
  { value: "/a/%2E%2e/%2e%2E/outside", want: undefined },
  { value: "/a/../keys?x=1", want: "/keys?x=1" },
  { value: "/../console/keys", want: "/keys" },
  { value: "/../sign-in", want: undefined },
  { value: "/keys/../sign-in?next=/x", want: undefined },
  { value: "//", want: undefined },
  { value: "//[", want: undefined },
  { value: "//evil.example:bad", want: undefined },
];

it.each([
  { value: "/keys", want: "/keys" },
  { value: "/calls?model=a%2Fb#top", want: "/calls?model=a%2Fb#top" },
  { value: "/", want: "/" },
  { value: "//evil.example/", want: undefined },
  { value: "/\\evil.example", want: undefined },
  { value: "https://evil.example/", want: undefined },
  { value: "keys", want: undefined },
  { value: "/sign-in?next=/", want: undefined },
  { value: "/mfa", want: undefined },
  { value: "/register?invite=x", want: undefined },
  { value: "/sign-ins", want: "/sign-ins" },
  { value: undefined, want: undefined },
  { value: ["/keys"], want: undefined },
])("the way back after signing in is $value → $want", ({ value, want }) => {
  expect(safeNext(value)).toBe(want);
});

it.each(dots)("under a base, the way back after signing in is $value → $want", ({ value, want }) => {
  expect(safeNext(value, "/console/")).toBe(want);
});

it("under the root, a path that climbs out of it is the root", () => {
  expect(safeNext("/../keys", "/")).toBe("/keys");
  expect(safeNext("/%2e%2e//evil.example", "/")).toBeUndefined();
});

it("malformed addresses are dropped under the root too", () => {
  for (const value of ["//", "//[", "//evil.example:bad", "/%2e%2e//["]) {
    expect(safeNext(value, "/")).toBeUndefined();
  }
});
