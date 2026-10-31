import { beforeEach, expect, it, vi } from "vitest";
import { forgetPending, takePending } from "./pending-codes-store";

const binding = "user/account/token";

beforeEach(() => forgetPending(binding));

it("takes once for a session, also when the effect runs twice", async () => {
  const send = vi.fn(async () => ({ recovery_codes: ["code-1", "code-2"] }));
  const first = takePending(binding, 0, send);
  const second = takePending(binding, 0, send);
  expect(send).toHaveBeenCalledTimes(1);
  await expect(first).resolves.toEqual({ codes: ["code-1", "code-2"] });
  await expect(second).resolves.toEqual({ codes: ["code-1", "code-2"] });
});

it("gives the answer to a caller that mounts after it arrived", async () => {
  // The page that asked is gone when the answer arrives (a route change, or StrictMode's first effect); the only copy
  // must reach the next caller, not a second take that would return nothing.
  const send = vi.fn(async () => ({ recovery_codes: ["code-1"] }));
  await takePending(binding, 0, send);
  await expect(takePending(binding, 0, send)).resolves.toEqual({ codes: ["code-1"] });
  expect(send).toHaveBeenCalledTimes(1);
});

it("keeps a failure for a retry or a later mount", async () => {
  const send = vi.fn(() => Promise.reject(new Error("offline")));
  const settled = await takePending(binding, 0, send);
  expect(settled).toMatchObject({ failure: expect.any(Error) });
  await takePending(binding, 0, send);
  expect(send).toHaveBeenCalledTimes(1);
});

it("takes again for a retry, another session, or after the result is forgotten", async () => {
  const send = vi.fn(async () => ({ recovery_codes: [] }));
  await takePending(binding, 0, send);
  await takePending(binding, 1, send);
  await takePending("user/other/token", 0, send);
  forgetPending("user/other/token");
  await takePending("user/other/token", 0, send);
  expect(send).toHaveBeenCalledTimes(4);
});
