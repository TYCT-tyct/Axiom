import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { expect, it } from "vitest";
import { versionQuery } from "./version";

it("aborts the call once nobody watches the query", () => {
  let signal: AbortSignal | undefined;
  const query = versionQuery((callSignal) => {
    signal = callSignal;
    return new Promise(() => {});
  });
  const unsubscribe = new QueryObserver(new QueryClient(), query).subscribe(() => {});
  expect(signal?.aborted).toBe(false);
  unsubscribe();
  expect(signal?.aborted).toBe(true);
});
