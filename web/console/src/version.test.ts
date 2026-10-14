import { consoleApi } from "@axiom/api";
import { QueryClient } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { versionQuery } from "./version";

it("hands the query's signal to the request, so a query nobody watches stops its call", async () => {
  const get = vi.spyOn(consoleApi, "GET").mockResolvedValue({
    data: { version: "v1" },
    response: new Response(),
  } as Awaited<ReturnType<typeof consoleApi.GET>>);
  await new QueryClient().fetchQuery(versionQuery);
  expect(get).toHaveBeenCalledWith("/api/console/v1/version", { signal: expect.any(AbortSignal) });
});
