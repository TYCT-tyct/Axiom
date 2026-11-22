import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
vi.mock("@axiom/api", () => ({
  consoleApi: { GET: get },
  unwrap: async (response: unknown) => response,
}));

import { legalQuery } from "./queries";

afterEach(() => vi.clearAllMocks());

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
