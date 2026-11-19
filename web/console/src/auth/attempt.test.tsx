// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it } from "vitest";
import { type Attempt, useAttempt } from "./attempt";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The page's request as a component holds it, read after each change.
let held: Attempt;
function Probe() {
  held = useAttempt();
  return null;
}

let unmount = () => {};
async function mount() {
  const root = createRoot(document.createElement("div"));
  await act(async () => root.render(<Probe />));
  unmount = () => act(() => root.unmount());
}

afterEach(() => unmount());

// A request whose outcome the test gives, and the signal it was handed.
function deferred() {
  const out: { signal?: AbortSignal; resolve: () => void; reject: (error: unknown) => void } = {
    resolve: () => {},
    reject: () => {},
  };
  const work = (signal: AbortSignal) =>
    new Promise<void>((resolve, reject) => Object.assign(out, { signal, resolve, reject }));
  return { work, out };
}

it("holds one request at a time, keeps its failure for the next to clear, and names the one that succeeded", async () => {
  await mount();
  const first = deferred();
  const heard: unknown[] = [];
  await act(async () => held.run("code", first.work, { onFail: (error) => heard.push(error) }));
  expect(held.kind).toBe("code");
  const second = deferred();
  await act(async () => held.run("passkey", second.work));
  expect(second.out.signal).toBeUndefined();
  await act(async () => first.out.reject("wrong"));
  expect(held).toMatchObject({ kind: null, failure: { kind: "code", error: "wrong" } });
  expect(heard).toEqual(["wrong"]);
  await act(async () => held.run("passkey", second.work));
  expect(held.failure).toBeNull();
  await act(async () => second.out.resolve());
  expect(held).toMatchObject({ kind: null, done: "passkey", failure: null });
});

it("drops the outcome of a cancelled request and frees the page at once", async () => {
  await mount();
  const request = deferred();
  const heard: unknown[] = [];
  await act(async () => held.run("oauth", request.work, { cancellable: true, onFail: (e) => heard.push(e) }));
  await act(async () => held.cancel?.());
  expect(held.kind).toBeNull();
  expect(request.out.signal?.aborted).toBe(true);
  await act(async () => request.out.reject(new DOMException("aborted", "AbortError")));
  expect(held.failure).toBeNull();
  expect(heard).toEqual([]);
});

it("offers no cancel for a request that may set the session's cookie", async () => {
  await mount();
  await act(async () => held.run("password", deferred().work));
  expect(held.kind).toBe("password");
  expect(held.cancel).toBeNull();
});

it("ends what is out when the page goes", async () => {
  await mount();
  const request = deferred();
  await act(async () => held.run("passkey", request.work, { cancellable: true }));
  unmount();
  unmount = () => {};
  expect(request.out.signal?.aborted).toBe(true);
});
