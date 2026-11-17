import { ApiError } from "@axiom/api";
import { QueryObserver } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { createQueryClient } from "./providers";
import { accountKey, type Session } from "./session";

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

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const refused = () => new ApiError(401, "request", { body: { error: { code: "unauthenticated" } } });

const replacements = [
  [
    "another account",
    session({ user: { id: "user_b", email: "b@example.com", invite_code: "x" }, csrf_token: "csrf-2" }),
  ],
  ["the same person's next session", session({ csrf_token: "csrf-2" })],
] as const;

describe("a late unauthenticated answer", () => {
  it.each(replacements)("does not end %s after a mutation started under the session before it", async (_, now) => {
    const client = createQueryClient();
    client.setQueryData(["session"], session());
    const answer = deferred<void>();
    const mutation = client.getMutationCache().build(client, { mutationFn: () => answer.promise });
    const settled = mutation.execute(undefined).catch((error: unknown) => error);

    client.setQueryData(["session"], now);
    answer.reject(refused());
    await settled;

    expect(client.getQueryData(["session"])).toEqual(now);
  });

  it.each(replacements)("does not end %s after a query started under the session before it", async (_, now) => {
    const client = createQueryClient();
    client.setQueryData(["session"], session());
    const answer = deferred<string>();
    const query = client
      .getQueryCache()
      .build(client, { queryKey: [accountKey, "late"], queryFn: () => answer.promise });
    const settled = query.fetch().catch((error: unknown) => error);

    client.setQueryData(["session"], now);
    answer.reject(refused());
    await settled;

    expect(client.getQueryData(["session"])).toEqual(now);
  });

  it("keeps the binding of the request in flight when a duplicate fetch reuses it", async () => {
    const client = createQueryClient();
    client.setQueryData(["session"], session());
    const answer = deferred<string>();
    let calls = 0;
    const query = client.getQueryCache().build(client, {
      queryKey: [accountKey, "duplicate"],
      queryFn: () => {
        calls += 1;
        return answer.promise;
      },
    });
    const first = query.fetch().catch((error: unknown) => error);

    const now = replacements[0][1];
    client.setQueryData(["session"], now);
    const second = query.fetch(undefined, { cancelRefetch: false }).catch((error: unknown) => error);
    expect(calls).toBe(1);

    answer.reject(refused());
    await Promise.all([first, second]);

    expect(client.getQueryData(["session"])).toEqual(now);
  });

  it("ends the session a mutation started under", async () => {
    const client = createQueryClient();
    client.setQueryData(["session"], session());
    const answer = deferred<void>();
    const mutation = client.getMutationCache().build(client, { mutationFn: () => answer.promise });
    const settled = mutation.execute(undefined).catch((error: unknown) => error);

    answer.reject(refused());
    await settled;

    expect(client.getQueryData(["session"])).toBeNull();
  });

  it("ends the session a query started under", async () => {
    const client = createQueryClient();
    client.setQueryData(["session"], session());
    const answer = deferred<string>();
    const query = client
      .getQueryCache()
      .build(client, { queryKey: [accountKey, "current"], queryFn: () => answer.promise });
    const settled = query.fetch().catch((error: unknown) => error);

    answer.reject(refused());
    await settled;

    expect(client.getQueryData(["session"])).toBeNull();
  });
});

describe("a refused read is asked for once", () => {
  const cases = [
    ["a refusal", new ApiError(403, "request"), 1],
    ["a network failure", new ApiError(0, null), 2],
  ] as const;

  it.each(cases)("%s: calls made when a second subscriber mounts after it", async (_, error, want) => {
    const client = createQueryClient();
    let calls = 0;
    const options = {
      queryKey: [accountKey, "balance"],
      queryFn: () => {
        calls += 1;
        return Promise.reject(error);
      },
      retry: false,
    };
    const first = new QueryObserver(client, options);
    const stopFirst = first.subscribe(() => undefined);
    await vi.waitFor(() => expect(first.getCurrentResult().isError).toBe(true));

    const second = new QueryObserver(client, options);
    const stopSecond = second.subscribe(() => undefined);
    await vi.waitFor(() => expect(second.getCurrentResult().fetchStatus).toBe("idle"));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(calls).toBe(want);
    stopFirst();
    stopSecond();
  });
});
