// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createInstance } from "i18next";
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { KeyForm, type KeyValues } from "./key-form";
import RoutingPresets from "./pages/routing-presets";
import { DisablePresetDialog } from "./preset-dialogs";

// What the mocked console answers, which a test changes between reads: the presets a key may use (or a failed read),
// the account's default, and every default the page sends.
const api = vi.hoisted(() => ({
  presetsFail: false,
  defaultFail: false,
  routingDefault: { preset_id: "", version: 0 } as { preset_id?: string; version: number },
  sent: [] as unknown[],
}));

const preset = (n: number) => ({
  id: `00000000-0000-4000-9000-00000000000${n}`,
  scope: "account",
  slug: `p${n}`,
  name: `Preset ${n}`,
  revision: 1,
  version: 1,
  rules: { default: {} },
  disabled: false,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
});

vi.mock("@axiom/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/api")>();
  const answer = (status: number, data: unknown) =>
    Promise.resolve(
      status === 200
        ? { data, response: new Response(null, { status }) }
        : { error: { error: { code: data, message: "" } }, response: new Response(null, { status }) },
    );
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      GET: vi.fn((path: string) => {
        switch (path) {
          case "/api/console/v1/routing-presets":
            return api.presetsFail ? answer(503, "unavailable") : answer(200, { data: [preset(1), preset(2)] });
          case "/api/console/v1/routing-default":
            return api.defaultFail ? answer(503, "unavailable") : answer(200, api.routingDefault);
          case "/api/console/v1/balance":
            return answer(500, "internal");
          default:
            return Promise.reject(new Error(`unexpected GET ${path}`));
        }
      }),
      PUT: vi.fn((_path: string, { body }: { body: { preset_id: string | null; version: number } }) => {
        api.sent.push(body);
        if (body.version !== api.routingDefault.version) {
          return answer(409, "stale_revision");
        }
        api.routingDefault = { preset_id: body.preset_id ?? undefined, version: api.routingDefault.version + 1 };
        return answer(200, api.routingDefault);
      }),
    },
  };
});

vi.mock("@axiom/app", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@axiom/app")>();
  return { ...actual, can: () => true, useSession: () => ({}) };
});

const i18n = createInstance();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(async () => {
  await i18n.init({ lng: "en", resources: {} });
  // jsdom has the dialog element but not its modal methods.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
});

beforeEach(() => {
  api.presetsFail = false;
  api.defaultFail = false;
  api.routingDefault = { preset_id: preset(1).id, version: 1 };
  api.sent = [];
});

afterEach(() => {
  document.body.replaceChildren();
});

// settle lets answered reads reach the page: the query cache tells its observers on a later task, after act's own.
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 0)));

async function render(node: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const container = document.createElement("div");
  document.body.append(container);
  await act(async () => {
    createRoot(container).render(
      <QueryClientProvider client={client}>
        <I18nextProvider i18n={i18n}>{node}</I18nextProvider>
      </QueryClientProvider>,
    );
  });
  await settle();
  return { client, container };
}

// choose picks an option the way a user does: the browser sets the value, then reports the change.
async function choose(select: HTMLSelectElement, value: string) {
  await act(async () => {
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

async function submit(form: HTMLFormElement) {
  await act(async () => {
    form.requestSubmit();
  });
  await settle();
}

// A preset chosen for a new key is sent even when the list is read again and that read fails: the choice must not fall
// back to the layers' default, which may not limit what the chosen preset limits.
it("keeps a key's chosen preset through a failed read of the list", async () => {
  const sent: KeyValues[] = [];
  const { client, container } = await render(
    <KeyForm pending={false} error={null} onSubmit={(values) => sent.push(values)} onCancel={() => {}} />,
  );
  const select = container.querySelector<HTMLSelectElement>("select[name=preset_id]");
  expect(select).not.toBeNull();
  await choose(select as HTMLSelectElement, preset(2).id);
  api.presetsFail = true;
  await act(async () => {
    await client.refetchQueries({ queryKey: [accountKey, "routing-presets"] });
  });
  await settle();
  expect(client.getQueryState([accountKey, "routing-presets"])?.status).toBe("error");
  (container.querySelector("input[name=name]") as HTMLInputElement).value = "Bot";
  await submit(container.querySelector("form") as HTMLFormElement);
  expect(sent.map((values) => values.preset_id)).toEqual([preset(2).id]);
});

// A management key calls no model and the server refuses it a preset, so its form offers none.
it("offers no preset for a management key", async () => {
  const key = {
    id: "key_1",
    kind: "management",
    name: "Ops",
    prefix: "sk-ax-v1-",
    last4: "1234",
    workspace_id: "ws_1",
    created_at: "2026-11-01T00:00:00Z",
    disabled: false,
  } as Schemas["Key"];
  const { container } = await render(
    <KeyForm apiKey={key} pending={false} error={null} onSubmit={() => {}} onCancel={() => {}} />,
  );
  expect(container.querySelector("input[name=name]")).not.toBeNull();
  expect(container.querySelector("select[name=preset_id]")).toBeNull();
});

// The default is sent at the version its choice was made against. A background read that brings a newer default does
// not move that version, so the save is refused instead of overwriting. A reload that fails says so and keeps the
// refusal, so it can be tried again; one that succeeds takes the newer default as the base.
it("sends the default at the version the choice was made against", async () => {
  const { client, container } = await render(<RoutingPresets />);
  const form = container.querySelector("form") as HTMLFormElement;
  const select = form.querySelector("select") as HTMLSelectElement;
  await choose(select, preset(2).id);
  api.routingDefault = { preset_id: undefined, version: 2 };
  await act(async () => {
    await client.refetchQueries({ queryKey: [accountKey, "routing-presets", "default"] });
  });
  await settle();
  await submit(form);
  expect(api.sent).toEqual([{ preset_id: preset(2).id, version: 1 }]);
  expect(api.routingDefault).toEqual({ preset_id: undefined, version: 2 });
  const reload = () =>
    [...form.querySelectorAll("button")].find((b) => b.textContent === "routingPresets.defaults.reload");
  const clickReload = async () => {
    await act(async () => {
      reload()?.click();
    });
    await settle();
  };
  api.defaultFail = true;
  await clickReload();
  expect(form.querySelectorAll("[role=alert]")).toHaveLength(2);
  expect(select.value).toBe(preset(2).id);
  expect(reload()).toBeDefined();
  api.defaultFail = false;
  await clickReload();
  expect(form.querySelectorAll("[role=alert]")).toHaveLength(0);
  expect(select.value).toBe("");
  await choose(select, preset(2).id);
  await submit(form);
  expect(api.sent).toEqual([
    { preset_id: preset(2).id, version: 1 },
    { preset_id: preset(2).id, version: 2 },
  ]);
  expect(api.routingDefault).toEqual({ preset_id: preset(2).id, version: 3 });
});

// Before disabling, the dialog says how many of the account's keys still route by the preset and, when it is the
// account's default for new keys (preset 1 here), that the default must be changed first; a node that counts no keys
// has no number said for it. The words are i18n keys: the test has no messages.
it.each([
  { name: "the default, with keys", n: 1, keys: 3, said: ["isDefault", "keys"] },
  { name: "no keys", n: 2, keys: 0, said: ["noKeys"] },
  { name: "a node that counts none", n: 2, keys: undefined, said: [] },
])("says what disabling meets: $name", async ({ n, keys, said }) => {
  const disabling: Schemas["RoutingPreset"] = { ...preset(n), scope: "account", rules: {}, keys };
  await render(<DisablePresetDialog preset={disabling} onClose={() => {}} />);
  const lines = [...document.querySelectorAll("dialog p")].map((p) => p.textContent);
  expect(lines).toEqual(["body", ...said].map((key) => `routingPresets.disable.${key}`));
});
