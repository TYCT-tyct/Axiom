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
import Keys from "./pages/keys";
import RoutingPresets from "./pages/routing-presets";
import { DisablePresetDialog } from "./preset-dialogs";

// What the mocked console answers, which a test changes between reads: the presets a key may use (or a failed read),
// the account's default, the account's keys, and every default the page sends.
const api = vi.hoisted(() => ({
  presetsFail: false,
  defaultFail: false,
  routingDefault: { preset_id: "", version: 0 } as Schemas["AccountRoutingDefault"],
  keys: [] as Schemas["Key"][],
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

// The default workspace's own preset, which its keys may choose and the account's default may not.
const workspacePreset = { ...preset(3), scope: "workspace", workspace_id: "ws_1" };

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
            return api.presetsFail
              ? answer(503, "unavailable")
              : answer(200, { data: [preset(1), preset(2), workspacePreset] });
          case "/api/console/v1/routing-default":
            return api.defaultFail ? answer(503, "unavailable") : answer(200, api.routingDefault);
          case "/api/console/v1/keys":
            return answer(200, { data: api.keys });
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

// The keys page keeps its filters in the address; here there are none.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => children,
  useNavigate: () => () => Promise.resolve(),
  useSearch: () => ({}),
}));

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
  api.keys = [];
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
// has no number said for it, then what disabling does. The warning never turns the button off: the page's default may be stale and the node
// decides (docs/domains/routing.md). The
// words are i18n keys: the test has no messages.
it.each([
  { name: "the default, with keys", n: 1, keys: 3, said: ["isDefault", "keys"] },
  { name: "no keys", n: 2, keys: 0, said: ["noKeys"] },
  { name: "a node that counts none", n: 2, keys: undefined, said: [] },
])("says what disabling meets: $name", async ({ n, keys, said }) => {
  const disabling: Schemas["RoutingPreset"] = { ...preset(n), scope: "account", rules: {}, keys };
  await render(<DisablePresetDialog preset={disabling} onClose={() => {}} />);
  const lines = [...document.querySelectorAll("dialog p")].map((p) => p.textContent);
  expect(lines).toEqual([...said, "body"].map((key) => `routingPresets.disable.${key}`));
  expect(document.querySelector<HTMLButtonElement>("dialog button[type=submit]")?.disabled).toBe(false);
});

const callKey = (name: string, bound?: number) =>
  ({
    id: `key_${name}`,
    kind: "call",
    name,
    prefix: "sk-ax-v1-",
    last4: "1234",
    workspace_id: "ws_1",
    created_at: "2026-11-01T00:00:00Z",
    disabled: false,
    preset_id: bound ? preset(bound).id : undefined,
    preset: bound ? { id: preset(bound).id, name: `Preset ${bound}`, scope: "account", version: 1 } : undefined,
  }) as Schemas["Key"];

const texts = (nodes: NodeListOf<Element>) => [...nodes].map((node) => node.textContent);

// The default's read names the preset a key made now takes (preset 2 here, the site's, as the account sets none) or
// says the default refuses new keys, and which layer decides; an older node says neither, and the pages say what they
// said before. The key form's first option and its mark on that preset, the default panel's "none" and warnings, the
// preset marked as the default for new keys, and the keys bound to another preset all follow the read. When the
// default workspace sets its own default, the panel says the account's does not reach new keys. The account's default
// never offers the workspace's preset, which a key may choose. A key without a preset is not compared: the node that
// listed it may give none.
it.each([
  {
    name: "a node that names it",
    read: { version: 0, new_keys_preset_id: preset(2).id },
    option: "byDefault",
    marked: [preset(2).id],
    none: "defaults.noneNamed",
    refused: [],
    badge: preset(2).id,
    moved: ["routingPresets.key.notDefaultNamed"],
  },
  {
    name: "a default of the default workspace",
    read: { preset_id: preset(1).id, version: 1, new_keys_preset_id: preset(2).id, new_keys_layer: "workspace" as const },
    option: "byDefault",
    marked: [preset(2).id],
    none: "defaults.none",
    refused: ["routingPresets.defaults.workspace"],
    badge: preset(2).id,
    moved: ["routingPresets.key.notDefaultNamed"],
  },
  {
    name: "a default workspace's default that refuses new keys",
    read: { version: 0, new_keys_refused: true, new_keys_layer: "workspace" as const },
    option: "byDefaultRefused",
    marked: [],
    none: "defaults.none",
    refused: ["routingPresets.defaults.refusedWorkspace"],
    badge: undefined,
    moved: [],
  },
  {
    name: "a default that refuses new keys",
    read: { version: 0, new_keys_refused: true },
    option: "byDefaultRefused",
    marked: [],
    none: "defaults.none",
    refused: ["routingPresets.defaults.refused"],
    badge: undefined,
    moved: [],
  },
  {
    name: "an older node",
    read: { version: 0 },
    option: "byLayers",
    marked: [],
    none: "defaults.none",
    refused: [],
    badge: undefined,
    moved: [],
  },
])("follows what new keys take: $name", async ({ read, option, marked, none, refused, badge, moved }) => {
  api.routingDefault = read;
  api.keys = [callKey("old", 1), callKey("current", 2), callKey("legacy")];
  const form = await render(<KeyForm pending={false} error={null} onSubmit={() => {}} onCancel={() => {}} />);
  const options = [...form.container.querySelectorAll<HTMLOptionElement>("select[name=preset_id] option")];
  expect(options[0]?.textContent).toBe(`routingPresets.key.${option}`);
  expect(options.filter((o) => o.textContent?.endsWith("routingPresets.key.current")).map((o) => o.value)).toEqual(
    marked,
  );
  expect(options.map((o) => o.value)).toContain(workspacePreset.id);
  document.body.replaceChildren();

  const page = await render(<RoutingPresets />);
  const defaultOptions = [...page.container.querySelectorAll<HTMLOptionElement>("form select option")];
  expect(defaultOptions[0]?.textContent).toBe(`routingPresets.${none}`);
  expect(defaultOptions.map((o) => o.value)).not.toContain(workspacePreset.id);
  expect(texts(page.container.querySelectorAll("form p.text-warn"))).toEqual(refused);
  // Each row's slug sits right under its name, and the badge after it.
  const badged = [...page.container.querySelectorAll("tbody tr")].filter((row) =>
    row.textContent?.includes("routingPresets.isDefault"),
  );
  expect(badged.map((row) => row.querySelector("p.font-mono")?.textContent)).toEqual(
    badge ? [`p${badge.slice(-1)}`] : [],
  );
  document.body.replaceChildren();

  // The rows ask for the default and the presets only once the keys are in.
  const keys = await render(<Keys />);
  await settle();
  // jsdom applies no breakpoints: every row has both the preset column and the labelled line narrow screens show under
  // the name, so a key bound elsewhere is noted twice and every call key is labelled once.
  const lines = texts(keys.container.querySelectorAll("td p"));
  expect(lines.filter((line) => line?.startsWith("routingPresets.key.notDefault"))).toEqual([...moved, ...moved]);
  expect(lines.filter((line) => line === "routingPresets.key.labelled")).toHaveLength(3);
});
