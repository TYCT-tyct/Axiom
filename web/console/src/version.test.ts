import { consoleApi } from "@axiom/api";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { versionQuery } from "./version";

it("aborts the call once nobody watches the query", () => {
  let signal: AbortSignal | undefined;
  vi.spyOn(consoleApi, "GET").mockImplementation((_path, init) => {
    signal = init?.signal ?? undefined;
    return new Promise(() => {});
  });
  const unsubscribe = new QueryObserver(new QueryClient(), versionQuery).subscribe(() => {});
  expect(signal?.aborted).toBe(false);
  unsubscribe();
  expect(signal?.aborted).toBe(true);
});
