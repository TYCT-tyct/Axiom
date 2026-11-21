import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  claimSession,
  consumeEmailChangeToken,
  holdsIdentity,
  holdsSession,
  redirectPath,
  type Session,
  safeNext,
  sessionBinding,
  stashEmailChangeToken,
  stripEmailChangeToken,
} from "./session";

const session = (over: Partial<Session> = {}) =>
  ({
    user: { id: "user_a", email: "a@example.com", invite_code: "invite" },
    account: "acct_a",
    level: "mfa",
    mfa_enabled: true,
    csrf_token: "csrf-1",
    expires_at: "2026-10-15T00:00:00Z",
    permissions: [],
    ...over,
  }) as Session;

const withSession = (held: Session | null) => {
  const client = new QueryClient();
  client.setQueryData(["session"], held);
  return client;
};

describe("a session claim", () => {
  it("names the person and the session the page holds when work starts", () => {
    expect(claimSession(withSession(session()))).toEqual({
      binding: "user_a/acct_a/csrf-1",
      identity: "user_a/acct_a",
    });
    expect(claimSession(withSession(null))).toBeNull();
  });

  it("holds while that session is current", () => {
    const client = withSession(session());
    expect(holdsSession(client, "user_a/acct_a/csrf-1")).toBe(true);
  });

  it.each([
    ["another account", session({ user: { id: "user_b", email: "b@example.com", invite_code: "x" } })],
    ["the same person's next session", session({ csrf_token: "csrf-2" })],
    ["no session", null],
  ])("drops an answer once the browser passes to %s", (_, now) => {
    const client = withSession(now);
    expect(holdsSession(client, sessionBinding(session()))).toBe(false);
  });

  it("never holds an answer nobody claimed", () => {
    expect(holdsSession(withSession(session()), "")).toBe(false);
  });

  it("keeps a factor change's answer across the session that change rotates", () => {
    const client = withSession(session());
    const claim = claimSession(client);
    if (!claim) {
      throw new Error("expected a session claim");
    }
    // The server rotated this person's session before the answer was read; only the token changed.
    client.setQueryData(["session"], session({ csrf_token: "csrf-2" }));
    expect(holdsIdentity(client, claim.identity)).toBe(true);
    expect(holdsSession(client, claim.binding)).toBe(false);
  });

  it.each([
    ["another account", session({ user: { id: "user_b", email: "b@example.com", invite_code: "x" } })],
    ["no session", null],
  ])("drops a factor change's answer once the browser passes to %s", (_, now) => {
    const client = withSession(now);
    expect(holdsIdentity(client, "user_a/acct_a")).toBe(false);
  });
});

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
  { value: "/consent?next=/keys", want: undefined },
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

it("strips email-change-token fragments before building next URLs and preserves other hashes", () => {
  expect(stripEmailChangeToken("/settings#email-change-token=secret-1")).toEqual({
    token: "secret-1",
    cleanHref: "/settings",
  });
  expect(stripEmailChangeToken("/settings?a=1#tab=2&email-change-token=secret-2")).toEqual({
    token: "secret-2",
    cleanHref: "/settings?a=1#tab=2",
  });
  expect(safeNext("/settings#email-change-token=example")).toBe("/settings");
  stashEmailChangeToken("secret-3");
  expect(consumeEmailChangeToken()).toBe("secret-3");
  expect(consumeEmailChangeToken()).toBeNull();
});

it("keeps guest-to-guest redirects free of nested ?next= while preserving protected destinations", () => {
  expect(redirectPath("pending", "guest", "/sign-in")).toEqual({ path: "/mfa", token: null });
  expect(redirectPath("guest", "pending", "/mfa")).toEqual({ path: "/sign-in", token: null });
  expect(redirectPath("pending", "guest", "/sign-in?next=%2Fkeys", "/keys")).toEqual({
    path: "/mfa?next=%2Fkeys",
    token: null,
  });
  expect(redirectPath("guest", "pending", "/mfa?next=%2Fkeys", "/keys")).toEqual({
    path: "/sign-in?next=%2Fkeys",
    token: null,
  });
  expect(redirectPath("guest", "member", "/settings#email-change-token=secret-4")).toEqual({
    path: "/sign-in?next=%2Fsettings",
    token: "secret-4",
  });
  expect(redirectPath("member", "guest", "/sign-in", "/keys")).toEqual({ path: "/keys", token: null });
});
