// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { act, type ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { KeyForm, type KeyValues } from "./key-form";
import Keys from "./pages/keys";
import RoutingPresets from "./pages/routing-presets";
import { DisablePresetDialog } from "./preset-dialogs";
import { choose, openRow, press, render, settle, setUpBoardPages, submit } from "./testing";

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
  const { answer, mockClient } = await import("./testing");
  return mockClient(importOriginal, {
    GET: (path: string) => {
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
    },
    PUT: (_path: string, options?: { body?: { preset_id: string | null; version: number } }) => {
      const body = options?.body as { preset_id: string | null; version: number };
      api.sent.push(body);
      if (body.version !== api.routingDefault.version) {
        return answer(409, "stale_revision");
      }
      api.routingDefault = { preset_id: body.preset_id ?? undefined, version: api.routingDefault.version + 1 };
      return answer(200, api.routingDefault);
    },
  });
});

// The keys page keeps its filters in the address; here there are none.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => children,
  useNavigate: () => () => Promise.resolve(),
  useSearch: () => ({}),
}));

vi.mock("@axiom/app", async (importOriginal) => (await import("./testing")).everyPermission(importOriginal));

setUpBoardPages();

beforeEach(() => {
  api.presetsFail = false;
  api.defaultFail = false;
  api.routingDefault = { preset_id: preset(1).id, version: 1 };
  api.keys = [];
  api.sent = [];
});

// The default's two buttons say what they do; with no messages in a test, they are their keys.
const pressDefault = (dialog: HTMLElement, which: "set" | "clear" | "reload") =>
  press(dialog, `routingPresets.defaults.${which}`);

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

// The default is sent at the version the page read. A change made elsewhere meanwhile is refused instead of
// overwritten: the page says so and loads the default as it now is, so the next press sends the current version.
it("sends the default at the version the page read and reloads it when it moved on", async () => {
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 1);
  await pressDefault(dialog, "set");
  expect(api.sent).toEqual([{ preset_id: preset(2).id, version: 1 }]);
  expect(api.routingDefault).toEqual({ preset_id: preset(2).id, version: 2 });
  // The page now shows preset 1 as the default, so the same button clears it.
  expect(dialog.textContent).toContain("routingPresets.defaults.saved");
  await pressDefault(dialog, "clear");
  expect(api.sent.at(-1)).toEqual({ preset_id: null, version: 2 });
});

// A default another tab changed: the version the page read went on, so the write is refused, the page says it loaded the
// latest, and the next press sends the version that is current.
it("refuses a stale default and sends the current version next", async () => {
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 1);
  api.routingDefault = { preset_id: undefined, version: 2 };
  await pressDefault(dialog, "set");
  expect(api.sent).toEqual([{ preset_id: preset(2).id, version: 1 }]);
  expect(dialog.textContent).toContain("routingPresets.defaults.reloaded");
  await pressDefault(dialog, "set");
  expect(api.sent.at(-1)).toEqual({ preset_id: preset(2).id, version: 2 });
  expect(api.routingDefault.version).toBe(3);
});

// A reload after a refusal can fail too: the page then says why and offers the reload again, and once it succeeds the
// next press sends the version the account has.
it("keeps the refusal when the reload fails, and tries it again", async () => {
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 1);
  api.routingDefault = { preset_id: undefined, version: 2 };
  api.defaultFail = true;
  await pressDefault(dialog, "set");
  expect(api.sent).toHaveLength(1);
  // A default that cannot be read offers no write, only the read again.
  expect(dialog.textContent).not.toContain("routingPresets.defaults.set");
  api.defaultFail = false;
  await pressDefault(dialog, "reload");
  // The read works again, so the write is offered again; the lines below say what the default now is.
  expect(dialog.textContent).toContain("routingPresets.defaults.set");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.reloaded");
  await pressDefault(dialog, "set");
  expect(api.sent.at(-1)).toEqual({ preset_id: preset(2).id, version: 2 });
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
  const lines = [...document.querySelectorAll("dialog p, dialog strong")].map((p) => p.textContent);
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
    read: {
      preset_id: preset(1).id,
      version: 1,
      new_keys_preset_id: preset(2).id,
      new_keys_layer: "workspace" as const,
    },
    option: "byDefault",
    marked: [preset(2).id],
    none: "defaults.none",
    refused: ["routingPresets.defaults.workspace"],
    badge: preset(2).id,
    moved: ["routingPresets.key.notDefaultNamed"],
  },
  {
    name: "a default workspace's default that refuses new keys",
    read: { preset_id: preset(1).id, version: 1, new_keys_refused: true, new_keys_layer: "workspace" as const },
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
  const dialog = await openRow(page.container, "routingPresets.openNamed");
  // What the account's own default leaves to the site or the platform, and what the account's does not decide.
  const said = [...dialog.querySelectorAll("p")].map((p) => p.textContent ?? "");
  const inherited = said.find((line) => line.startsWith("routingPresets.defaults.none"));
  // What the account's default leaves to the site or the platform is said only while it sets none.
  if (read.preset_id) {
    expect(inherited).toBeUndefined();
  } else {
    expect(inherited).toBe(`routingPresets.${none}`);
  }
  // The warnings are the shared Note's: its title is the sentence, in a <strong>.
  expect(texts(dialog.querySelectorAll("strong"))).toEqual(refused);
  // The preset a key made now takes is marked on its row, by its slug.
  const badged = [...page.container.querySelectorAll("tbody tr")].filter((row) =>
    row.textContent?.includes("routingPresets.isDefault"),
  );
  expect(badged.map((row) => row.querySelector(".mut")?.textContent?.split(" · ")[0])).toEqual(
    badge ? [`p${badge.slice(-1)}`] : [],
  );
  document.body.replaceChildren();

  // The rows ask for the default and the presets only once the keys are in. The list is the board's, which has no preset
  // column, so each key's own dialog names the preset it is routed by and says when that is not what a key made now
  // would take (L3 in /home/ai/axiom-ops/design/console-gap.md).
  const keys = await render(<Keys />);
  await settle();
  const rows = [...keys.container.querySelectorAll("tbody tr")];
  expect(rows).toHaveLength(3);
  const names: (string | undefined)[] = [];
  const notes: string[] = [];
  for (const row of rows) {
    await act(async () => {
      row.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settle();
    const text = keys.container.querySelector("dialog")?.textContent ?? "";
    names.push(/Preset \d|routingPresets\.key\.hidden/.exec(text)?.[0]);
    notes.push(...[...text.matchAll(/routingPresets\.key\.notDefault(?:Named)?/g)].map((match) => match[0]));
  }
  expect(names).toEqual(["Preset 1", "Preset 2", "routingPresets.key.hidden"]);
  expect(notes).toEqual([...moved]);
});
