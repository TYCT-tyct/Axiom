// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import { act, type ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import type { KeyFilters } from "../queries";
import { answer, render, settle, setUpPages, submit } from "../testing";
import Keys from "./keys";

// What the mocked console answers, which a test moves between reads: the account's keys, the filters the node says it
// applied, and every key the page writes back.
const api = vi.hoisted(() => ({
  keys: [] as Schemas["Key"][],
  filters: undefined as Schemas["KeyFilterEcho"] | undefined,
  asked: {} as Record<string, string | undefined>,
  sent: [] as Schemas["KeySettings"][],
  undoFails: false,
}));

const aKey = (over: Partial<Schemas["Key"]> = {}): Schemas["Key"] => ({
  id: "key_1",
  kind: "call",
  name: "Laptop",
  prefix: "sk-ax-v1-",
  last4: "1234",
  workspace_id: "ws_1",
  created_at: "2026-10-01T00:00:00Z",
  disabled: false,
  spend_limit: { amount: "20000000000", currency: "USD" },
  limit_remaining: { amount: "15000000000", currency: "USD" },
  limit_reset: "monthly",
  ...over,
});

// A write replaces every setting the page sends, as the contract does, and answers with the key as it now is.
function apply(body: Schemas["KeySettings"], id: string) {
  api.keys = api.keys.map((key) => (key.id === id ? { ...key, ...body } : key));
  return api.keys.find((key) => key.id === id);
}

// Every write the page makes is recorded; undoing a switch is the one this file can make fail.
function write(
  _path: string,
  { params, body }: { params: { path: { key_id: string } }; body: Schemas["KeySettings"] },
) {
  api.sent.push(body);
  if (api.undoFails && body.disabled === false && api.sent.length > 1) {
    return answer(409, "stale_revision");
  }
  return answer(200, apply(body, params.path.key_id));
}

vi.mock("@axiom/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/api")>();
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      GET: vi.fn((path: string) => {
        switch (path) {
          case "/api/console/v1/keys":
            return answer(200, { data: api.keys, filters: api.filters });
          case "/api/console/v1/workspaces":
            return answer(200, { data: [] });
          case "/api/console/v1/routing-presets":
            return answer(200, { data: [] });
          case "/api/console/v1/routing-default":
            return answer(200, { version: 0 });
          case "/api/console/v1/balance":
            return answer(500, "internal");
          default:
            return Promise.reject(new Error(`unexpected GET ${path}`));
        }
      }),
      PUT: vi.fn(write),
      DELETE: vi.fn(() => answer(200, undefined)),
    },
  };
});

// The page reads its filters from the address and writes them back; here the address is the test's own object, and a
// link is the text it would carry.
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => api.asked as KeyFilters,
  useNavigate: () => async () => undefined,
  Link: (props: { children: ReactNode }) => props.children,
}));

vi.mock("@axiom/app", async (importOriginal) => (await import("../testing")).everyPermission(importOriginal));

setUpPages();

beforeEach(() => {
  api.keys = [aKey()];
  api.filters = undefined;
  api.asked = {};
  api.sent = [];
  api.undoFails = false;
});

async function open() {
  const page = await render(<Keys />);
  await settle();
  return page.container;
}

// The row a key is shown in, clicked as a user clicks it.
async function openKey(container: HTMLElement, index = 0) {
  const row = container.querySelectorAll("tbody tr")[index] as HTMLElement;
  await act(async () => {
    row.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await settle();
  return container.querySelector("dialog") as HTMLElement;
}

it("shows the rows a node applied the filters to", async () => {
  api.asked = { name: "Lap" };
  api.filters = { name: "Lap" };
  expect((await open()).textContent).toContain("Laptop");
});

// A node before key filters ignores the parameters and echoes none: its list is every key, and showing it as the
// filtered result would say the filters matched rows they never saw.
it("shows no rows from a node that did not apply the filters", async () => {
  api.asked = { name: "nothing-matches" };
  api.filters = undefined;
  const text = (await open()).textContent ?? "";
  expect(text).not.toContain("Laptop");
  expect(text).toContain("keys.filtersUnavailable.title");
});

// Switching a key off and then changing its settings: the notice still offers the switch back, on the key as it is now,
// so undoing keeps the settings saved after the switch rather than writing the older ones back.
it("undoes a switch on the key as it is now, and says why when that fails", async () => {
  const container = await open();
  const detail = await openKey(container);
  await act(async () => {
    (detail.querySelector('input[name="disabled"]') as HTMLInputElement).click();
  });
  await submit(detail.querySelector("form") as HTMLFormElement);
  expect(api.sent[0]).toMatchObject({ disabled: true });
  expect(container.textContent).toContain("keys.board.disabledNotice");

  // A second visit tightens the limit; the notice stays, and the undo must not undo this.
  const again = await openKey(container);
  await act(async () => {
    (again.querySelector('input[name="spend_limit"]') as HTMLInputElement).value = "7.5";
  });
  await submit(again.querySelector("form") as HTMLFormElement);
  expect(api.sent[1]).toMatchObject({ spend_limit: { amount: "7500000000" } });

  // The undo fails: the page says why and offers the button again.
  api.undoFails = true;
  const undo = () => container.querySelector("button") as HTMLButtonElement;
  const button = [...container.querySelectorAll("button")].find((b) => b.textContent === "keys.board.undo");
  await act(async () => {
    (button as HTMLButtonElement).click();
  });
  await settle();
  expect(api.sent[2]).toMatchObject({ disabled: false, name: "Laptop", spend_limit: { amount: "7500000000" } });
  expect(container.textContent).toContain("errors.stale_revision");
  expect(undo()).toBeDefined();
});
