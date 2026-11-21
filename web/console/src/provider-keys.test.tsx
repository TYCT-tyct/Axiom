// @vitest-environment jsdom
import { ApiError, type Schemas } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { act } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import ProviderKeys from "./pages/provider-keys";
import { failRead, fill, press, render, settle, setUpBoardPages } from "./testing";

type ProviderKey = Schemas["ProviderKey"];

// What the mocked console answers: the account's keys, which a test changes between reads, and what a test or a removal
// came back with. A removal a test holds answers only when that test lets it.
const api = vi.hoisted(() => ({
  keys: [] as ProviderKey[],
  denied: [] as string[],
  removed: [] as string[],
  tested: [] as string[],
  held: undefined as undefined | (() => void),
}));

const key = (n: number, over: Partial<ProviderKey>): ProviderKey => ({
  id: `00000000-0000-4000-9000-00000000000${n}`,
  name: `Key ${n}`,
  provider: "openai",
  hint: n === 1 ? "Qx7d" : "Ab12",
  status: "active",
  enabled: true,
  rejected: false,
  fallback: true,
  created_at: "2026-10-01T00:00:00Z",
  ...over,
});

const usual = () => [key(1, {}), key(2, { name: "Spare", rejected: true, status: "rejected", fallback: false })];

vi.mock("@axiom/api", async (importOriginal) => {
  const { answer, mockClient } = await import("./testing");
  return mockClient(importOriginal, {
    GET: (path: string) =>
      path === "/api/console/v1/provider-keys" ? answer(200, { data: api.keys }) : answer(503, "unavailable"),
    POST: (_path: string, options?: { params?: { path?: { provider_key_id?: string } } }) => {
      api.tested.push(options?.params?.path?.provider_key_id ?? "");
      return answer(200, { ok: false, status: "rejected" });
    },
    PUT: (_path: string, options?: { params?: { path?: { provider_key_id?: string } } }) => {
      const id = options?.params?.path?.provider_key_id;
      api.keys = api.keys.map((entry) => (entry.id === id ? { ...entry, enabled: !entry.enabled } : entry));
      return answer(
        200,
        api.keys.find((entry) => entry.id === id),
      );
    },
    DELETE: (_path: string, options?: { params?: { path?: { provider_key_id?: string } } }) => {
      const id = options?.params?.path?.provider_key_id ?? "";
      api.removed.push(id);
      return new Promise((resolve) => {
        const done = () => {
          api.keys = api.keys.filter((entry) => entry.id !== id);
          void resolve(answer(200, undefined));
        };
        if (api.held) {
          api.held = done;
        } else {
          done();
        }
      });
    },
  });
});

vi.mock("@axiom/app", async (importOriginal) =>
  (await import("./testing")).sessionWith(importOriginal, (permission) => !api.denied.includes(permission)),
);

setUpBoardPages();

beforeEach(() => {
  api.keys = usual();
  api.denied = [];
  api.removed = [];
  api.tested = [];
  api.held = undefined;
});

const dialog = () => document.querySelector("dialog") as HTMLElement;

// A key's last four characters and how routing sees it are what tells two keys of one provider apart: the row says
// both, and whether the key may fall back to the site's endpoints — whether or not it has a hint.
it("lists the account's keys with their last four characters, state and fallback", async () => {
  const { container } = await render(<ProviderKeys />);
  const rows = [...container.querySelectorAll(".qrow")];
  expect(rows).toHaveLength(2);
  expect(rows[0]?.textContent).toContain("Key 1");
  expect(rows[0]?.textContent).toContain("•••• Qx7d");
  expect(rows[0]?.textContent).toContain("keys.enabled");
  expect(rows[0]?.textContent).toContain("providerKeys.fallbackOn");
  // A key the provider refused says so with its hint, and one with no fallback says that too.
  expect(rows[1]?.textContent).toContain("•••• Ab12");
  expect(rows[1]?.textContent).toContain("providerKeys.rejected");
  expect(rows[1]?.textContent).toContain("providerKeys.noFallback");
});

// A test that fails says the provider refused the key, which is what routing then skips; the node keeps the provider's
// own answer out of the page.
it("says so when the provider refuses a key", async () => {
  const { container } = await render(<ProviderKeys />);
  await press(container, "providerKeys.openNamed");
  await press(dialog(), "providerKeys.test.action");
  expect(api.tested).toEqual([api.keys[0]?.id]);
  expect(dialog().textContent).toContain("providerKeys.test.refused");
});

// The dialog shows the key as the list has it now: a switch applied here must be readable and reversible here, not only
// after the dialog is opened again.
it("shows a key as it now is after switching it", async () => {
  const { container } = await render(<ProviderKeys />);
  await press(container, "providerKeys.openNamed");
  await press(dialog(), "providerKeys.disable");
  expect(dialog().textContent).toContain("keys.disabled");
  await press(dialog(), "providerKeys.enable");
  expect(dialog().textContent).toContain("keys.enabled");
});

// Removal is a confirmation away, and nothing is sent before it is given: the key's sealed value is gone for good.
it("removes a key only after the confirmation", async () => {
  const { container } = await render(<ProviderKeys />);
  await press(container, "providerKeys.openNamed");
  await press(dialog(), "providerKeys.remove");
  expect(api.removed).toEqual([]);
  expect(dialog().textContent).toContain("providerKeys.removeBody");
  const id = api.keys[0]?.id;
  await press(dialog(), "providerKeys.confirmRemove");
  expect(api.removed).toEqual([id]);
});

// A removal that is still running must not close a form opened after it: its completion only closes the dialog that
// asked, and the name already typed into the new one stays.
it("does not close a form opened while a removal is still running", async () => {
  api.held = () => {};
  const { container } = await render(<ProviderKeys />);
  await press(container, "providerKeys.openNamed");
  await press(dialog(), "providerKeys.remove");
  const id = api.keys[0]?.id;
  await press(dialog(), "providerKeys.confirmRemove");
  expect(api.removed).toEqual([id]);
  await act(async () => (dialog() as HTMLDialogElement).close());
  await settle();
  await press(container, "providerKeys.add");
  const form = dialog().querySelector("form") as HTMLFormElement;
  await fill(form.querySelector("input") as HTMLInputElement, "Second key");
  api.held?.();
  await settle();
  expect(dialog()).not.toBeNull();
  expect((dialog().querySelector("input") as HTMLInputElement).value).toBe("Second key");
});

// A session that loses the permission, or a node that refuses the read, keeps what the query cache holds: the list and
// any dialog go, and the page says why.
it("hides the keys read before the permission was taken away", async () => {
  const { client, container } = await render(<ProviderKeys />);
  expect(container.textContent).toContain("Key 1");
  api.denied = ["api_keys.view"];
  // Another read brings the same keys back: they are still not shown without the permission.
  await act(async () => {
    client.setQueryData([accountKey, "provider-keys"], { data: [...api.keys, key(3, {})] });
  });
  await settle();
  expect(container.textContent).not.toContain("Key 1");
  expect(container.textContent).toContain("providerKeys.noPermission.title");
  // The add action goes with the rows: no form can be opened behind the closed dialog.
  expect(container.textContent).not.toContain("providerKeys.add");
});

// A read that fails for another reason keeps the page as it is: a form being filled stays, and the list offers the retry.
it("keeps a form open when the keys cannot be read", async () => {
  const { client, container } = await render(<ProviderKeys />);
  await press(container, "providerKeys.add");
  await fill(dialog().querySelector("input") as HTMLInputElement, "Second key");
  await failRead(client, [accountKey, "provider-keys"]);
  await settle();
  expect(document.querySelector("dialog")).not.toBeNull();
  expect((document.querySelector("dialog") as HTMLElement).querySelector("input")?.value).toBe("Second key");
});

it("shows a node's refusal instead of the keys it read before", async () => {
  const { client, container } = await render(<ProviderKeys />);
  expect(container.textContent).toContain("Key 1");
  await act(async () => {
    const query = client.getQueryCache().find({ queryKey: [accountKey, "provider-keys"] });
    query?.setState({ status: "error", error: new ApiError(403, null) });
  });
  await settle();
  expect(container.textContent).not.toContain("Key 1");
});

it("renders the page heading before the empty state inside the page frame", async () => {
  api.keys = [];
  const { container } = await render(<ProviderKeys />);
  const headings = [...container.querySelectorAll("h1, h2")].map((node) => node.textContent);
  expect(container.firstElementChild?.className).toContain("page");
  expect(headings.indexOf("nav.providerKeys")).toBe(0);
  expect(headings.indexOf("providerKeys.empty.title")).toBeGreaterThan(0);
});
