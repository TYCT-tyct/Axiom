import { expect, it } from "vitest";
import { safeNext } from "./session";

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
