// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import { beforeEach, expect, it, vi } from "vitest";
import ProviderKeys from "./pages/provider-keys";
import { press, render, setUpBoardPages } from "./testing";

type ProviderKey = Schemas["ProviderKey"];

// What the mocked console answers: the account's keys, and what a test or a removal came back with.
const api = vi.hoisted(() => ({
  removed: [] as string[],
  tested: [] as string[],
}));

const key = (n: number, over: Partial<ProviderKey>): ProviderKey => ({
  id: `00000000-0000-4000-9000-00000000000${n}`,
  name: `Key ${n}`,
  provider: "openai",
  hint: "Qx7d",
  status: "active",
  enabled: true,
  rejected: false,
  fallback: true,
  created_at: "2026-10-01T00:00:00Z",
  ...over,
});

const keys = [
  key(1, {}),
  key(2, { name: "Spare", hint: undefined, rejected: true, status: "rejected", fallback: false }),
];

vi.mock("@axiom/api", async (importOriginal) => {
  const { answer, mockClient } = await import("./testing");
  return mockClient(importOriginal, {
    GET: (path: string) =>
      path === "/api/console/v1/provider-keys" ? answer(200, { data: keys }) : answer(503, "unavailable"),
    POST: (_path: string, options?: { params?: { path?: { provider_key_id?: string } } }) => {
      api.tested.push(options?.params?.path?.provider_key_id ?? "");
      return answer(200, { ok: false, status: "rejected" });
    },
    PUT: () => answer(200, keys[0]),
    DELETE: (_path: string, options?: { params?: { path?: { provider_key_id?: string } } }) => {
      api.removed.push(options?.params?.path?.provider_key_id ?? "");
      return answer(200, undefined);
    },
  });
});

vi.mock("@axiom/app", async (importOriginal) => (await import("./testing")).everyPermission(importOriginal));

setUpBoardPages();

beforeEach(() => {
  api.removed = [];
  api.tested = [];
});

// A key's last four characters and how routing sees it are what tells two keys of one provider apart: the row says
// both, and whether the key may fall back to the site's endpoints.
it("lists the account's keys with their last four characters, state and fallback", async () => {
  const { container } = await render(<ProviderKeys />);
  const rows = [...container.querySelectorAll(".qrow")];
  expect(rows).toHaveLength(2);
  expect(rows[0]?.textContent).toContain("Key 1");
  expect(rows[0]?.textContent).toContain("•••• Qx7d");
  expect(rows[0]?.textContent).toContain("providerKeys.fallbackOn");
  // A key the provider refused says so, and one with no hint keeps its provider and state.
  expect(rows[1]?.textContent).toContain("providerKeys.rejected");
  expect(rows[1]?.textContent).toContain("providerKeys.noFallback");
});

// A test that fails says the provider refused the key, which is what routing then skips; the node keeps the provider's
// own answer out of the page.
it("says so when the provider refuses a key", async () => {
  const { container } = await render(<ProviderKeys />);
  await press(container, "providerKeys.openNamed");
  const dialog = document.querySelector("dialog") as HTMLElement;
  await press(dialog, "providerKeys.test.action");
  expect(api.tested).toEqual([keys[0]?.id]);
  expect(dialog.textContent).toContain("providerKeys.test.refused");
});

// Removal is a confirmation away, and nothing is sent before it is given: the key's sealed value is gone for good.
it("removes a key only after the confirmation", async () => {
  const { container } = await render(<ProviderKeys />);
  await press(container, "providerKeys.openNamed");
  const dialog = document.querySelector("dialog") as HTMLElement;
  await press(dialog, "providerKeys.remove");
  expect(api.removed).toEqual([]);
  expect(dialog.textContent).toContain("providerKeys.removeBody");
  await press(dialog, "providerKeys.confirmRemove");
  expect(api.removed).toEqual([keys[0]?.id]);
});
