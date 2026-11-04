import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
vi.mock("@axiom/api", () => ({
  consoleApi: { GET: get },
  unwrap: async (response: unknown) => response,
}));
vi.mock("@axiom/app", () => ({ accountKey: "account" }));

import { keysQuery, legalQuery, recentErrorsQuery } from "./queries";

afterEach(() => vi.clearAllMocks());

describe("call queries", () => {
  it("asks the server for five recent errors instead of filtering a normal page", async () => {
    const data = { data: [], next_cursor: null, status_filter: "error" };
    get.mockResolvedValue(data);
    const client = new QueryClient();
    expect(await client.fetchQuery(recentErrorsQuery)).toBe(data);
    expect(get).toHaveBeenCalledWith("/api/console/v1/calls", {
      params: { query: { status: "error", limit: 5 } },
      signal: expect.any(AbortSignal),
    });
    client.clear();
  });
});

describe("key queries", () => {
  it("sends the filters to the server and keeps them in the key, because a cursor is only good for its filters", async () => {
    const data = { data: [], next_cursor: null };
    get.mockResolvedValue(data);
    const filters = { name: "alpha", status: "disabled" } as const;
    const client = new QueryClient();
    expect((await client.fetchInfiniteQuery(keysQuery(filters))).pages).toEqual([data]);
    expect(get).toHaveBeenCalledWith("/api/console/v1/keys", {
      params: { query: { ...filters, cursor: undefined, limit: 50 } },
      signal: expect.any(AbortSignal),
    });
    expect(keysQuery(filters).queryKey).not.toEqual(keysQuery({}).queryKey);
    client.clear();
  });
});

describe("legal query", () => {
  it("asks for every published locale instead of the old one-per-kind response", async () => {
    const data = { documents: [] };
    get.mockResolvedValue(data);
    const client = new QueryClient();
    expect(await client.fetchQuery(legalQuery)).toBe(data);
    expect(get).toHaveBeenCalledWith("/api/console/v1/legal", {
      params: { query: { locales: "all" } },
      signal: expect.any(AbortSignal),
    });
    client.clear();
  });
});
