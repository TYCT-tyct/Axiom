import { consoleApi } from "@axiom/api";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { versionQuery } from "./version";

it("aborts the call once nobody watches the query", () => {
  let signal: AbortSignal | undefined;
  vi.spyOn(consoleApi, "GET").mockImplementation((_path, init) => {
    // GET is generic over every path of the contract, and the init of all of them at once is not one type.
    signal = (init as RequestInit | undefined)?.signal ?? undefined;
    return new Promise(() => {});
  });
  const unsubscribe = new QueryObserver(new QueryClient(), versionQuery).subscribe(() => {});
  expect(signal?.aborted).toBe(false);
  unsubscribe();
  expect(signal?.aborted).toBe(true);
});
