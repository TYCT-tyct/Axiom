// @vitest-environment jsdom
import { ApiError } from "@axiom/api";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act, useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { type SettingDrafts, SettingsSubmit, type SettingValue, useSettingDrafts } from "./settings-drafts";
import type { SettingKey } from "./settings-registry";
import { renderInDom, typeInto } from "./test-dom";

// Two operations: the first saves a.one and a.two, the second b.one.
vi.mock("./settings-registry", () => {
  const settingsRegistry = {
    "a.one": { page: "site", operation: "updateSiteSettings", label: "test.a1" },
    "a.two": { page: "site", operation: "updateSiteSettings", label: "test.a2" },
    "b.one": { page: "site", operation: "updateSiteRegistration", label: "test.b1" },
  };
  return { settingsRegistry, settingKeys: Object.keys(settingsRegistry) };
});
vi.mock("@axiom/app", async (original) => (await import("./test-dom")).signedIn(original));

// The mocked keys are not the real registry's, so the test holds them as plain strings.
type Key = SettingKey;
type Values = Record<string, SettingValue>;
const refused = new ApiError(422, null, { body: { error: { code: "invalid_request" } } });
const stale = new ApiError(409, null, { body: { error: { code: "stale_revision" } } });
const done = { data: null, replayed: false };

let drafts: SettingDrafts<Key>;
let setSaved: (saved: Values) => void;
const writes = { updateSiteSettings: vi.fn(), updateSiteRegistration: vi.fn() };

function Page() {
  const [saved, set] = useState<Values>({ "a.one": "0", "a.two": "0", "b.one": "0" });
  setSaved = set;
  drafts = useSettingDrafts<Key>({ scope: `settings:test:${Math.random()}`, saved, writes } as never);
  return <SettingsSubmit drafts={drafts} />;
}

async function open() {
  const root = createRootRoute({ component: Outlet });
  const routes = [
    createRoute({ getParentRoute: () => root, path: "/", component: Page }),
    createRoute({ getParentRoute: () => root, path: "/other", component: () => null }),
  ];
  const router = createRouter({
    routeTree: root.addChildren(routes),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  const view = await renderInDom(<RouterProvider router={router} />);
  await vi.waitFor(() => expect(drafts).toBeDefined());
  mounted.push(view.unmount);
  return router;
}

const mounted: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const unmount of mounted.splice(0)) await unmount();
  writes.updateSiteSettings.mockReset();
  writes.updateSiteRegistration.mockReset();
});

const button = (name: string) =>
  [...document.querySelectorAll("button")].find((found) => found.textContent?.trim().startsWith(name));
const dialogText = () => document.querySelector("dialog")?.textContent ?? "";

async function submit(reason?: string) {
  const dialog = document.querySelector("dialog") as HTMLDialogElement;
  if (reason) await typeInto(dialog.querySelector("textarea") as HTMLTextAreaElement, reason);
  await act(async () => (dialog.querySelector("button[type=submit]") as HTMLButtonElement).click());
}

async function edit(values: Values) {
  await act(async () => {
    for (const [key, value] of Object.entries(values)) drafts.set(key as Key, value as SettingValue);
  });
}

it("sends each operation in turn under its own key, clears the one that went and keeps the one that failed", async () => {
  await open();
  writes.updateSiteSettings.mockResolvedValue(done);
  writes.updateSiteRegistration.mockRejectedValueOnce(refused).mockResolvedValueOnce(done);
  await edit({ "b.one": "2", "a.one": "1" });
  expect(document.body.textContent).toContain("2 changes not submitted");
  // ⌘S opens the review, as its button does
  await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "s", metaKey: true })));
  expect(document.querySelectorAll("dialog ins")).toHaveLength(2);
  await submit("Routine maintenance");
  await vi.waitFor(() => expect(dialogText()).toContain("Not submitted: test.b1. failed"));
  expect(dialogText()).toContain("Submitted: test.a1");
  const [first] = writes.updateSiteSettings.mock.calls;
  const [second] = writes.updateSiteRegistration.mock.calls;
  expect(first?.[0]).toMatchObject({ "a.one": "1", "a.two": "0", "b.one": "0" });
  expect(first?.[1].reason).toBe("Routine maintenance");
  expect(second?.[0]).toMatchObject({ "b.one": "2", "a.one": "0" });
  expect(second?.[1].key).not.toBe(first?.[1].key);
  expect(writes.updateSiteSettings.mock.invocationCallOrder[0]).toBeLessThan(
    writes.updateSiteRegistration.mock.invocationCallOrder[0] ?? 0,
  );
  expect(drafts.changed).toEqual(["b.one"]);
  expect(drafts.failure("b.one" as Key)).toBe(refused);
  // a second submit sends only what is left
  await submit();
  await vi.waitFor(() => expect(writes.updateSiteRegistration).toHaveBeenCalledTimes(2));
  expect(writes.updateSiteSettings).toHaveBeenCalledTimes(1);
});

it("sends a lost operation again with its key and does not send again the one already done", async () => {
  await open();
  writes.updateSiteSettings.mockResolvedValue(done);
  writes.updateSiteRegistration.mockRejectedValueOnce(new ApiError(503, null, {})).mockResolvedValueOnce(done);
  await edit({ "a.one": "1", "b.one": "2" });
  await act(async () => button("Review and submit")?.click());
  await submit("Incident response");
  await vi.waitFor(() => expect(button("Resend as it was")).toBeDefined());
  await act(async () => button("Resend as it was")?.click());
  await vi.waitFor(() => expect(writes.updateSiteRegistration).toHaveBeenCalledTimes(2));
  expect(writes.updateSiteSettings).toHaveBeenCalledTimes(1);
  const [lost, again] = writes.updateSiteRegistration.mock.calls;
  expect(again?.[1].key).toBe(lost?.[1].key);
  expect(again?.[0]).toEqual(lost?.[0]);
});

it("never overwrites another change: a stale revision gives up that operation's drafts on reload", async () => {
  await open();
  writes.updateSiteRegistration.mockRejectedValue(stale);
  await edit({ "b.one": "2" });
  await act(async () => button("Review and submit")?.click());
  await submit("Routine maintenance");
  await vi.waitFor(() => expect(dialogText()).toContain("Someone else changed this"));
  await act(async () => button("Reload")?.click());
  expect(drafts.changed).toEqual([]);
  expect(writes.updateSiteRegistration).toHaveBeenCalledTimes(1);
});

it("holds back a draft whose saved value moved since it was made", async () => {
  await open();
  await edit({ "a.one": "1" });
  await act(async () => setSaved({ "a.one": "5", "a.two": "0", "b.one": "0" }));
  await act(async () => button("Review and submit")?.click());
  await submit("Routine maintenance");
  expect(dialogText()).toContain("Someone else changed test.a1 after you did");
  expect(writes.updateSiteSettings).not.toHaveBeenCalled();
});

it("reverts one row, and asks before the page is left with drafts", async () => {
  const router = await open();
  await edit({ "a.one": "1", "a.two": "3" });
  await act(async () => drafts.revert("a.two" as Key));
  expect(drafts.changed).toEqual(["a.one"]);
  await act(async () => void router.navigate({ to: "/other" }));
  await vi.waitFor(() => expect(dialogText()).toContain("Changes not submitted"));
  await act(async () => button("Stay")?.click());
  expect(router.state.location.pathname).toBe("/");
  await act(async () => void router.navigate({ to: "/other" }));
  await vi.waitFor(() => expect(button("Leave")).toBeDefined());
  await act(async () => button("Leave")?.click());
  await vi.waitFor(() => expect(router.state.location.pathname).toBe("/other"));
});
