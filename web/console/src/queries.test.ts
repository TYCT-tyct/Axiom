import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
vi.mock("@axiom/api", () => ({
  consoleApi: { GET: get },
  unwrap: async (response: unknown) => response,
}));
vi.mock("@axiom/app", () => ({ accountKey: "account" }));

import { recentErrorsQuery } from "./queries";

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
