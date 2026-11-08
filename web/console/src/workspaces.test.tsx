// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import type { ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { SecretDialog } from "./key-dialogs";
import { KeyForm, type KeyValues } from "./key-form";
import Workspaces from "./pages/workspaces";
import { choose, render, settle, setUpPages, submit } from "./testing";
import { BudgetDialog } from "./workspace-dialogs";

type Workspace = Schemas["Workspace"];

// What the mocked console answers, which a test sets: the account's workspaces (or a status for a node without them),
// a workspace's budget (or a status), the permissions the session lacks, and every budget the page sends. Presets: the
// default workspace ws_1 offers p1 and its own p2 and gives new keys p1; ws_2 offers p1 and its own p3 and gives p3.
const api = vi.hoisted(() => ({
  workspaces: [] as Workspace[] | number,
  budget: {} as Schemas["WorkspaceBudget"] | number,
  denied: [] as string[],
  sent: [] as unknown[],
  read: [] as string[],
}));

const presetId = (n: number) => `00000000-0000-4000-9000-00000000000${n}`;

vi.mock("@axiom/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/api")>();
  const { answer } = await import("./testing");
  const read = (value: unknown, data: (value: never) => unknown) =>
    typeof value === "number" ? answer(value, "not_found") : answer(200, data(value as never));
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      GET: vi.fn((path: string, options?: { params?: { query?: object; path?: object } }) => {
        const where = { ...options?.params?.query, ...options?.params?.path } as { workspace_id?: string };
        const preset = (n: number) => ({
          id: `00000000-0000-4000-9000-00000000000${n}`,
          scope: "account",
          name: `P${n}`,
        });
        switch (path) {
          case "/api/console/v1/routing-presets":
            api.read.push(`presets ${where.workspace_id ?? "default"}`);
            return answer(200, { data: where.workspace_id ? [preset(1), preset(3)] : [preset(1), preset(2)] });
          case "/api/console/v1/routing-default":
            api.read.push("default default");
            return answer(200, { version: 1, new_keys_preset_id: preset(1).id });
          case "/api/console/v1/workspaces/{workspace_id}/routing-default":
            api.read.push(`default ${where.workspace_id}`);
            return answer(200, { version: 1, new_keys_preset_id: preset(3).id });
          case "/api/console/v1/workspaces":
            return read(api.workspaces, (data) => ({ data }));
          case "/api/console/v1/workspaces/{workspace_id}/budget":
            return read(api.budget, (data) => data);
          case "/api/console/v1/balance":
            return answer(200, { balance: { amount: "0", currency: "USD" } });
          case "/api/console/v1/site":
            return answer(200, { api_base_url: "" });
          default:
            return answer(503, "unavailable");
        }
      }),
      PUT: vi.fn((_path: string, { body }: { body: Schemas["WorkspaceBudgetSettings"] }) => {
        api.sent.push(body);
        return answer(200, body);
      }),
    },
  };
});

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => children,
}));

vi.mock("@axiom/app", async (importOriginal) => {
  const session = await (await import("./testing")).everyPermission(importOriginal);
  return { ...session, can: (_session: unknown, permission: string) => !api.denied.includes(permission) };
});

const workspace = (n: number, extra: Partial<Workspace> = {}): Workspace => ({
  id: `ws_${n}`,
  name: `Space ${n}`,
  is_default: n === 1,
  disabled: false,
  ...extra,
});

setUpPages();

beforeEach(() => {
  api.workspaces = [workspace(1)];
  api.budget = {};
  api.denied = [];
  api.sent = [];
  api.read = [];
});

// An account with one workspace makes keys as before, with no workspace field; with several, a key goes to the one the
// page was looking at unless keys cannot be made there, and a disabled workspace is not offered.
it.each([
  { name: "one workspace", workspaces: [workspace(1)], wanted: undefined, offered: null, sent: undefined },
  {
    name: "several, none wanted",
    workspaces: [workspace(1), workspace(2), workspace(3, { disabled: true })],
    wanted: undefined,
    offered: ["ws_1", "ws_2"],
    sent: "ws_1",
  },
  {
    name: "several, one wanted",
    workspaces: [workspace(1), workspace(2)],
    wanted: "ws_2",
    offered: ["ws_1", "ws_2"],
    sent: "ws_2",
  },
  {
    name: "several, a disabled one wanted",
    workspaces: [workspace(1), workspace(2, { disabled: true }), workspace(3)],
    wanted: "ws_2",
    offered: ["ws_1", "ws_3"],
    sent: "ws_1",
  },
])("makes a key in the workspace chosen: $name", async ({ workspaces, wanted, offered, sent }) => {
  api.workspaces = workspaces;
  const values: KeyValues[] = [];
  const { container } = await render(
    <KeyForm workspace={wanted} pending={false} error={null} onSubmit={(v) => values.push(v)} onCancel={() => {}} />,
  );
  const select = container.querySelector<HTMLSelectElement>("select[name=workspace_id]");
  expect(select && [...select.options].map((option) => option.value)).toEqual(offered);
  (container.querySelector("input[name=name]") as HTMLInputElement).value = "Bot";
  await submit(container.querySelector("form") as HTMLFormElement);
  expect(values.map((v) => v.workspace_id)).toEqual([sent]);
});

// Each workspace has presets and a default of its own: choosing one asks for its presets and its default, and a preset
// chosen in another workspace is dropped rather than sent where the server refuses it.
it("offers the presets of the workspace chosen", async () => {
  api.workspaces = [workspace(1), workspace(2)];
  const values: KeyValues[] = [];
  const { container } = await render(
    <KeyForm pending={false} error={null} onSubmit={(v) => values.push(v)} onCancel={() => {}} />,
  );
  const presets = () => container.querySelector("select[name=preset_id]") as HTMLSelectElement;
  const offered = () => [...presets().options].map((option) => option.value);
  expect(offered()).toEqual(["", presetId(1), presetId(2)]);
  await choose(presets(), presetId(2));
  await choose(container.querySelector("select[name=workspace_id]") as HTMLSelectElement, "ws_2");
  await settle();
  expect(api.read).toEqual(expect.arrayContaining(["presets ws_2", "default ws_2"]));
  expect(offered()).toEqual(["", presetId(1), presetId(3)]);
  expect(presets().value).toBe("");
  (container.querySelector("input[name=name]") as HTMLInputElement).value = "Bot";
  await submit(container.querySelector("form") as HTMLFormElement);
  expect(values.map((v) => [v.workspace_id, v.preset_id])).toEqual([["ws_2", undefined]]);
});

// A workspace the user chose and another tab then disabled stays chosen: the form says why and refuses to submit, rather
// than making the key in the default workspace, against its budget.
it("keeps a chosen workspace that was disabled and refuses to submit", async () => {
  api.workspaces = [workspace(1), workspace(2)];
  const values: KeyValues[] = [];
  const { client, container } = await render(
    <KeyForm pending={false} error={null} onSubmit={(v) => values.push(v)} onCancel={() => {}} />,
  );
  const field = () => container.querySelector("select[name=workspace_id]") as HTMLSelectElement;
  await choose(field(), "ws_2");
  api.workspaces = [workspace(1), workspace(2, { disabled: true })];
  await client.refetchQueries({ queryKey: ["account", "workspaces"] });
  await settle();
  expect(field().value).toBe("ws_2");
  expect(container.textContent).toContain("workspaces.key.lost");
  (container.querySelector("input[name=name]") as HTMLInputElement).value = "Bot";
  await submit(container.querySelector("form") as HTMLFormElement);
  expect(values).toEqual([]);
  await choose(field(), "ws_1");
  await submit(container.querySelector("form") as HTMLFormElement);
  expect(values.map((v) => v.workspace_id)).toEqual(["ws_1"]);
});

// A node before workspaces makes the key in the default workspace whatever was asked; the dialog that shows the key
// says so, and says nothing when the key is where it was asked for or no workspace was asked.
it.each([
  { name: "made where asked", asked: "ws_2", warned: false },
  { name: "nothing asked", asked: undefined, warned: false },
  { name: "made in the default instead", asked: "ws_3", warned: true },
])("says when a key was made elsewhere: $name", async ({ asked, warned }) => {
  api.workspaces = [workspace(1), workspace(2), workspace(3)];
  const key = { id: "key_1", name: "Bot", workspace_id: asked === undefined ? "ws_1" : "ws_2" } as Schemas["Key"];
  const { container } = await render(
    <SecretDialog created={{ key, secret: "sk-test" }} asked={asked} onClose={() => {}} />,
  );
  expect(container.ownerDocument.body.textContent?.includes("workspaces.key.moved")).toBe(warned);
});

// A node before workspaces answers 404: the page says workspaces are unavailable and offers nothing to make.
it("says workspaces are unavailable on a node without them", async () => {
  api.workspaces = 404;
  const { container } = await render(<Workspaces />);
  expect(container.textContent).toContain("workspaces.unavailable.title");
  expect(container.textContent).not.toContain("workspaces.create");
});

// Without api_keys.view the list is never asked for; the page says why instead of loading for ever.
it("says the session may not see workspaces", async () => {
  api.denied = ["api_keys.view"];
  const { container } = await render(<Workspaces />);
  expect(container.textContent).toContain("workspaces.noPermission.title");
  expect(container.textContent).not.toContain("workspaces.create");
});

it("lists the workspaces with the default marked", async () => {
  api.workspaces = [workspace(1), workspace(2, { disabled: true })];
  const { container } = await render(<Workspaces />);
  const rows = [...container.querySelectorAll("tbody tr")].map((row) => row.textContent);
  expect(rows).toEqual([
    expect.stringContaining("Space 1workspaces.default"),
    expect.stringContaining("keys.disabled"),
  ]);
});

// The budget is replaced as a whole: an amount in the site's currency with its reset, or nothing to remove it.
it.each([
  {
    name: "a new amount",
    typed: "2.5",
    reset: "weekly",
    sent: { limit: { amount: "2500000000", currency: "USD" }, limit_reset: "weekly" },
  },
  { name: "no reset", typed: "1", reset: "", sent: { limit: { amount: "1000000000", currency: "USD" } } },
  { name: "nothing, to remove it", typed: "", reset: "daily", sent: {} },
])("sets a workspace's budget: $name", async ({ typed, reset, sent }) => {
  api.budget = {
    limit: { amount: "1000000000", currency: "USD" },
    limit_reset: "daily",
    used: { amount: "1500000000", currency: "USD" },
    limit_remaining: { amount: "0", currency: "USD" },
    resets_at: "2026-11-09T00:00:00Z",
  };
  const { container } = await render(<BudgetDialog workspace={workspace(2)} manage onClose={() => {}} />);
  const body = container.ownerDocument.body;
  expect(body.textContent).toContain("workspaces.budget.used");
  (body.querySelector("input[name=limit]") as HTMLInputElement).value = typed;
  (body.querySelector("select[name=limit_reset]") as HTMLSelectElement).value = reset;
  await submit(body.querySelector("form") as HTMLFormElement);
  expect(api.sent).toEqual([JSON.parse(JSON.stringify(sent))]);
});

it.each([
  { name: "a node without budgets", budget: 404, said: "workspaces.budget.unavailable", form: false },
  { name: "no budget", budget: {}, said: "workspaces.budget.none", form: true },
])("shows the budget: $name", async ({ budget, said, form }) => {
  api.budget = budget;
  const { container } = await render(<BudgetDialog workspace={workspace(2)} manage onClose={() => {}} />);
  const body = container.ownerDocument.body;
  expect(body.textContent).toContain(said);
  expect(body.querySelector("form") !== null).toBe(form);
});
