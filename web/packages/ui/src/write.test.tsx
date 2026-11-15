// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import { type ExplicitSave, type LiveSave, useExplicitSave, useLiveSave } from "./write";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// A fake API: every call is recorded with its body and stays out until the test answers it.
type Call<T> = { body: T; resolve: (value: T) => void; reject: (error: unknown) => void };
function fakeApi<T>() {
  const calls: Call<T>[] = [];
  const send = (body: T) =>
    new Promise<T>((resolve, reject) => {
      calls.push({ body, resolve, reject });
    });
  return { calls, send };
}

const failed = (status: number, code?: string) =>
  new ApiError(status, "req_1", { body: code ? { error: { code, message: code } } : undefined });

let root: Root;
let host: HTMLElement;
beforeEach(() => {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

// mount runs a hook in a component and hands back its latest result.
function mount<R>(hook: () => R) {
  const result = { current: undefined as R };
  function Probe() {
    result.current = hook();
    return null;
  }
  act(() => root.render(<Probe />));
  return result;
}

// Let the promises a call's answer settles run.
const settle = () => act(async () => {});

type Key = { name: string; limit: number | null };

it("saves the whole draft once, keeps it on a failure and sends the same request again", async () => {
  const api = fakeApi<Key>();
  const save = mount<ExplicitSave<Key>>(() =>
    useExplicitSave<Key>({ initial: { name: "ci", limit: null }, save: api.send }),
  );
  act(() => save.current.set({ name: "ci-runner", limit: 100 }));
  expect(save.current.dirty).toBe(true);

  act(() => {
    void save.current.save();
    void save.current.save();
  });
  expect(api.calls).toHaveLength(1);
  expect(save.current.saving).toBe(true);

  await act(async () => api.calls[0]?.reject(failed(503)));
  expect(save.current).toMatchObject({ saving: false, dirty: true, draft: { name: "ci-runner", limit: 100 } });
  expect(save.current.failure).toBeInstanceOf(ApiError);

  let outcome: unknown;
  act(() => {
    void save.current.save().then((done) => {
      outcome = done;
    });
  });
  expect(api.calls).toHaveLength(2);
  expect(api.calls[1]?.body).toEqual(api.calls[0]?.body);
  await act(async () => api.calls[1]?.resolve({ name: "ci-runner", limit: 100 }));
  expect(outcome).toEqual({ saved: { name: "ci-runner", limit: 100 } });
  expect(save.current).toMatchObject({ saving: false, dirty: false, failure: null });
});

it("counts a change typed back as no change", () => {
  const save = mount<ExplicitSave<Key>>(() =>
    useExplicitSave<Key>({ initial: { name: "ci", limit: null }, save: fakeApi<Key>().send }),
  );
  act(() => save.current.set({ name: "cix", limit: null }));
  act(() => save.current.set({ name: "ci", limit: null }));
  expect(save.current.dirty).toBe(false);
});

type Prefs = { revision: number; balance: boolean; digest: boolean };
const initial: Prefs = { revision: 1, balance: false, digest: false };

it("sends two quick changes one after another, the second on what the first returned", async () => {
  const api = fakeApi<Prefs>();
  const live = mount<LiveSave<Prefs>>(() => useLiveSave({ initial, write: api.send }));
  act(() => {
    live.current.change((p) => ({ ...p, balance: true }));
    live.current.change((p) => ({ ...p, digest: true }));
  });
  expect(api.calls).toHaveLength(1);
  expect(api.calls[0]?.body).toEqual({ revision: 1, balance: true, digest: false });
  expect(live.current).toMatchObject({ saving: true, value: { balance: true, digest: true } });

  await act(async () => api.calls[0]?.resolve({ revision: 2, balance: true, digest: false }));
  expect(api.calls).toHaveLength(2);
  expect(api.calls[1]?.body).toEqual({ revision: 2, balance: true, digest: true });

  await act(async () => api.calls[1]?.resolve({ revision: 3, balance: true, digest: true }));
  expect(live.current).toMatchObject({
    saving: false,
    saved: true,
    value: { revision: 3, balance: true, digest: true },
  });
});

it("reads the object again on 409 stale_revision and sends nothing made on the old copy", async () => {
  const api = fakeApi<Prefs>();
  const fresh: Prefs = { revision: 5, balance: false, digest: true };
  const live = mount<LiveSave<Prefs>>(() =>
    useLiveSave({ initial, write: api.send, reread: () => Promise.resolve(fresh) }),
  );
  act(() => {
    live.current.change((p) => ({ ...p, balance: true }));
    live.current.change((p) => ({ ...p, digest: false }));
  });
  await act(async () => api.calls[0]?.reject(failed(409, "stale_revision")));
  await settle();
  expect(api.calls).toHaveLength(1);
  expect(live.current).toMatchObject({ saving: false, stale: true, failure: null, value: fresh });
});

it.each([
  { case: "a server error", error: failed(500, "internal") },
  { case: "no response", error: failed(0) },
  { case: "a 409 with no way to read again", error: failed(409, "stale_revision") },
])("on $case, says why and shows the server's object again", async ({ error }) => {
  const api = fakeApi<Prefs>();
  const live = mount<LiveSave<Prefs>>(() => useLiveSave({ initial, write: api.send }));
  act(() => live.current.change((p) => ({ ...p, balance: true })));
  expect(live.current.value.balance).toBe(true);
  await act(async () => api.calls[0]?.reject(error));
  expect(live.current).toMatchObject({ saving: false, saved: false, stale: false, failure: error, value: initial });

  act(() => live.current.change((p) => ({ ...p, digest: true })));
  expect(api.calls[1]?.body).toEqual({ ...initial, digest: true });
  expect(live.current.failure).toBeNull();
});
