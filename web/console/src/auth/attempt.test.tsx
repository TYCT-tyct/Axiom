// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it } from "vitest";
import { type Attempts, useAttempts } from "./attempt";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The page's attempts as a component holds them, read after each change.
let held: Attempts;
function Probe() {
  held = useAttempts();
  return null;
}

let unmount = () => {};
async function mount() {
  const root = createRoot(document.createElement("div"));
  await act(async () => root.render(<Probe />));
  unmount = () => act(() => root.unmount());
}

afterEach(() => unmount());

it("holds one attempt at a time, until it is done or fails", async () => {
  await mount();
  let first = held.begin("passkey");
  await act(async () => {});
  expect(held.kind).toBe("passkey");
  expect(held.begin("password")).toBeNull();
  await act(async () => first?.fail(new Error("refused")));
  expect(held).toMatchObject({ kind: null, failure: { kind: "passkey" } });
  first = held.begin("password");
  await act(async () => {});
  expect(held.failure).toBeNull();
  await act(async () => first?.done());
  expect(held.kind).toBeNull();
});

it("ends the attempt that took the visitor away when the browser brings the page back", async () => {
  await mount();
  const attempt = held.begin("oauth");
  await act(async () => {});
  await act(async () => {
    window.dispatchEvent(Object.assign(new Event("pageshow"), { persisted: true }));
  });
  expect(held.kind).toBeNull();
  expect(attempt?.live()).toBe(false);
  expect(attempt?.signal.aborted).toBe(true);
  // its late answer changes nothing
  await act(async () => attempt?.fail(new Error("late")));
  expect(held.failure).toBeNull();
});

it("cancels what is out when the page goes", async () => {
  await mount();
  const attempt = held.begin("passkey");
  await act(async () => {});
  unmount();
  unmount = () => {};
  expect(attempt?.signal.aborted).toBe(true);
  expect(attempt?.live()).toBe(false);
});
