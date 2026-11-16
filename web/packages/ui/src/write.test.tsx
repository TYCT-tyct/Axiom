// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import { act, StrictMode } from "react";
import { expect, it, vi } from "vitest";
import { testRoot } from "./test-root";
import { type ExplicitSave, type LiveSave, useExplicitSave, useLiveSave } from "./write";

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
const dom = testRoot();

// mount runs a hook in a component and hands back its latest result.
function mount<R>(hook: () => R) {
  const result = { current: undefined as R };
  function Probe() {
    result.current = hook();
    return null;
  }
  // StrictMode as the apps start: effects run, are cleaned up and run again on mount.
  act(() =>
    dom.root.render(
      <StrictMode>
        <Probe />
      </StrictMode>,
    ),
  );
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

// live mounts useLiveSave on a fake API; reread answers with what the server holds now, or fails.
function live(server: () => Promise<Prefs> = () => Promise.resolve(initial)) {
  const api = fakeApi<Prefs>();
  const reread = vi.fn(server);
  const save = mount<LiveSave<Prefs>>(() => useLiveSave({ initial, write: api.send, reread }));
  return { api, reread, save };
}

// changeTwo makes two quick changes, the second before the first has an answer.
const changeTwo = (save: { current: LiveSave<Prefs> }) =>
  act(() => {
    save.current.change((p) => ({ ...p, balance: true }));
    save.current.change((p) => ({ ...p, digest: true }));
  });

it("sends two quick changes one after another, the second on what the first returned", async () => {
  const { api, save } = live();
  changeTwo(save);
  expect(api.calls).toHaveLength(1);
  expect(api.calls[0]?.body).toEqual({ revision: 1, balance: true, digest: false });
  expect(save.current).toMatchObject({ saving: true, value: { balance: true, digest: true } });

  await act(async () => api.calls[0]?.resolve({ revision: 2, balance: true, digest: false }));
  expect(api.calls).toHaveLength(2);
  expect(api.calls[1]?.body).toEqual({ revision: 2, balance: true, digest: true });

  await act(async () => api.calls[1]?.resolve({ revision: 3, balance: true, digest: true }));
  expect(save.current).toMatchObject({
    saving: false,
    saved: true,
    value: { revision: 3, balance: true, digest: true },
  });
});

it("reads the object again on 409 stale_revision and sends nothing made on the old copy", async () => {
  const fresh: Prefs = { revision: 5, balance: false, digest: true };
  const { api, save } = live(() => Promise.resolve(fresh));
  act(() => {
    save.current.change((p) => ({ ...p, balance: true }));
    save.current.change((p) => ({ ...p, digest: false }));
  });
  await act(async () => api.calls[0]?.reject(failed(409, "stale_revision")));
  await settle();
  expect(api.calls).toHaveLength(1);
  expect(save.current).toMatchObject({ saving: false, locked: false, value: fresh, failed: { after: "reloaded" } });
});

// The page that made the edits is gone (another page, or another person signed in): nothing it queued is sent.
it("sends nothing queued once the page has gone", async () => {
  const { api, save } = live();
  changeTwo(save);
  act(() => dom.root.render(null));
  await act(async () => api.calls[0]?.resolve({ revision: 2, balance: true, digest: false }));
  expect(api.calls).toHaveLength(1);
});

it("shows the server's object again when the server refused the change", async () => {
  const { api, reread, save } = live();
  act(() => save.current.change((p) => ({ ...p, balance: true })));
  expect(save.current.value.balance).toBe(true);
  const refusal = failed(400, "invalid_request");
  await act(async () => api.calls[0]?.reject(refusal));
  expect(reread).not.toHaveBeenCalled();
  expect(save.current).toMatchObject({ saving: false, value: initial, failed: { error: refusal, after: "reverted" } });

  act(() => save.current.change((p) => ({ ...p, digest: true })));
  expect(api.calls[1]?.body).toEqual({ ...initial, digest: true });
  expect(save.current.failed).toBeNull();
});

// The server may have applied the change, or still be applying it, when its answer is lost. A read can come back
// before a timed-out write lands, so none confirms it: the next change would send the old value back and undo the first.
// Nothing more is sent until the page is reloaded.
it.each([
  { case: "no response", error: failed(0) },
  { case: "a server error", error: failed(500, "internal") },
])("after $case, sends nothing more and does not trust a read", async ({ error }) => {
  const { api, reread, save } = live();
  act(() => save.current.change((p) => ({ ...p, balance: true })));
  await act(async () => api.calls[0]?.reject(error));
  expect(reread).not.toHaveBeenCalled();
  expect(save.current).toMatchObject({ locked: true, saving: false, failed: { error, after: "unconfirmed" } });
  act(() => save.current.change((p) => ({ ...p, digest: true })));
  expect(api.calls).toHaveLength(1);
});

it("sends nothing more when a 409 cannot be followed by reading again", async () => {
  const { api, save } = live(() => Promise.reject(failed(0)));
  act(() => save.current.change((p) => ({ ...p, balance: true })));
  await act(async () => api.calls[0]?.reject(failed(409, "stale_revision")));
  await settle();
  expect(save.current).toMatchObject({ locked: true, saving: false, failed: { after: "unconfirmed" } });
  act(() => save.current.change((p) => ({ ...p, digest: true })));
  expect(api.calls).toHaveLength(1);
});
