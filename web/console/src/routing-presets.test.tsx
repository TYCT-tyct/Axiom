// @vitest-environment jsdom
import { ApiError, type Schemas } from "@axiom/api";
import { accountKey } from "@axiom/app";
import { act } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import RoutingPresets from "./pages/routing-presets";
import { failRead, fill, openRow, press, render, settle, setUpBoardPages } from "./testing";

type Preset = Schemas["RoutingPreset"];

// What the mocked console answers: the presets, the account's default (or a failure for it), and what a write to the
// default came back with.
const api = vi.hoisted(() => ({
  sent: [] as unknown[],
  status: 200,
  read: undefined as unknown,
  reads: 0,
  manage: true,
  view: true,
  presets: [] as Preset[],
  presetReads: 0,
  hold: undefined as undefined | "read" | "fail",
  release: undefined as undefined | (() => void),
}));

const id = (n: number) => `00000000-0000-4000-9000-00000000000${n}`;

function preset(n: number, scope: Preset["scope"], name: string, keys?: number): Preset {
  return {
    id: id(n),
    scope,
    slug: `p${n}`,
    name,
    revision: 1,
    version: n,
    keys,
    disabled: false,
    rules: {},
    summary: { sort: "price", verified_only: false, priced: false, overrides: 0 },
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };
}

const presets = [
  preset(1, "platform", "Official only", 1),
  preset(2, "site", "Verified"),
  preset(3, "account", "Cheapest first", 12),
  preset(4, "workspace", "Nightly"),
];

vi.mock("@axiom/api", async (importOriginal) => {
  const { answer, mockClient } = await import("./testing");
  return mockClient(importOriginal, {
    GET: (path: string) => {
      if (path === "/api/console/v1/routing-presets") {
        api.presetReads += 1;
        if (api.hold) {
          const how = api.hold;
          api.hold = undefined;
          return new Promise((resolve) => {
            api.release = () => {
              void resolve(how === "fail" ? answer(503, "unavailable") : answer(200, { data: api.presets }));
            };
          });
        }
        return answer(200, { data: api.presets });
      }
      if (path !== "/api/console/v1/routing-default") {
        return answer(503, "unavailable");
      }
      api.reads += 1;
      return api.read === undefined ? answer(503, "unavailable") : answer(200, api.read);
    },
    POST: () =>
      answer(200, { summary: { sort: "price", verified_only: false, priced: false, overrides: 0 }, unmatched: [] }),
    PUT: (path: string, options?: { body?: unknown; params?: { path?: { preset_id?: string } } }) => {
      api.sent.push(options?.body);
      // A preset's own change names the revision it read, which a change made elsewhere meanwhile moves on.
      if (path.startsWith("/api/console/v1/routing-presets/")) {
        const body = options?.body as { revision: number };
        const server = api.presets.find((entry) => entry.id === options?.params?.path?.preset_id);
        if (body.revision !== server?.revision) {
          return answer(409, "stale_revision");
        }
        api.presets = api.presets.map((entry) =>
          entry.id === server?.id ? { ...entry, revision: entry.revision + 1 } : entry,
        );
        return answer(200, { ...server, revision: (server?.revision ?? 1) + 1 });
      }
      const body = options?.body as { preset_id: string | null };
      return api.status === 200
        ? answer(200, { preset_id: body.preset_id ?? undefined, version: 3 })
        : answer(500, "internal");
    },
  });
});

vi.mock("@axiom/app", async (importOriginal) => {
  const session = await (await import("./testing")).everyPermission(importOriginal);
  return {
    ...session,
    can: (_session: unknown, permission: string) => (permission === "api_keys.manage" ? api.manage : api.view),
  };
});

setUpBoardPages();

beforeEach(() => {
  api.sent = [];
  api.presets = presets.map((entry) => ({ ...entry }));
  api.presetReads = 0;
  api.hold = undefined;
  api.release = undefined;
  api.status = 200;
  api.read = { preset_id: id(3), version: 2, new_keys_preset_id: id(3) };
  api.reads = 0;
  api.manage = true;
  api.view = true;
});

// openEditor opens the preset at that row and its editor, which every change to a preset starts from.
async function openEditor(container: HTMLElement, index: number) {
  await openRow(container, "routingPresets.openNamed", index);
  await press(document.querySelector("dialog") as HTMLElement, "routingPresets.actions.edit");
  return document.querySelector("dialog") as HTMLElement;
}

// Each preset is one row of the board's table: its name and slug, the layer it comes from, its rules in a line, the
// account's keys bound to it and its rules' version.
it("lists the presets with their layer, rules, keys and version, and marks the default", async () => {
  const { container } = await render(<RoutingPresets />);
  const rows = [...container.querySelectorAll("tbody tr")];
  expect(rows).toHaveLength(4);
  const cells = (row: Element) => [...row.children].map((cell) => cell.textContent);
  const first = cells(rows[0] as Element);
  expect(first).toHaveLength(5);
  expect(first[0]).toContain("Official only");
  expect(first[0]).toContain("p1");
  expect(first[1]).toBe("routingPresets.layer.platform");
  expect(first[2]).toBe("presetRules.sorts.price · presetRules.summary.allClasses");
  expect(first[3]).toBe("1");
  expect(first[4]).toBe("1");
  // The account's preset is what a new key takes, which its slug line says.
  expect(rows[2]?.textContent).toContain("p3 · routingPresets.isDefault");
});

// The dialog says what the key's row does not: how many of the account's keys are bound to the preset, and what a key
// made now takes.
it("says how many keys are bound and what new keys take", async () => {
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 2);
  expect(dialog.textContent).toContain("Cheapest first");
  expect(dialog.textContent).toContain("routingPresets.keysBound");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.none");
});

// The account's default is the preset it names. A workspace's own default is what a key made now takes there, which the
// account's default cannot override: opening that preset must not offer to "clear" a default the account never set to
// it, and clearing the account's default is a control of its own.
it("clears the account's default, never a workspace's", async () => {
  api.read = { preset_id: id(1), version: 2, new_keys_preset_id: id(4), new_keys_layer: "workspace" };
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 3);
  expect(dialog.textContent).toContain("routingPresets.defaults.clearAccount");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.set");
  await press(dialog, "routingPresets.defaults.clearAccount");
  expect(api.sent).toEqual([{ preset_id: null, version: 2 }]);
  // Clearing says only that: what a key made now takes is read from the default, not promised by the message.
  expect(dialog.textContent).toContain("routingPresets.defaults.cleared");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.reloaded");
});

// A workspace's preset cannot be the account's default, which the node refuses: no button offers it.
it("does not offer a workspace's preset as the account's default", async () => {
  api.read = { preset_id: null, version: 0 };
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 3);
  expect(dialog.textContent).not.toContain("routingPresets.defaults.set");
});

// With the account setting none, the preset the site or the platform gives can be made the account's own: the page must
// not offer only a way to clear a default it does not have.
it("sets a platform or site preset as the account's default", async () => {
  api.read = { preset_id: null, version: 0, new_keys_preset_id: id(2) };
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 1);
  await press(dialog, "routingPresets.defaults.set");
  expect(api.sent).toEqual([{ preset_id: id(2), version: 0 }]);
});

// The write is based on the read: until the default is read there is nothing to name a version with, and a read that
// failed offers the reload instead of a write.
it("does not offer a write before the default was read", async () => {
  api.read = undefined;
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 1);
  expect(dialog.textContent).not.toContain("routingPresets.defaults.set");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.clearAccount");
  await press(dialog, "routingPresets.defaults.reload");
  expect(api.reads).toBeGreaterThan(1);
});

// A success and then a failure are two readings of one try: the page says the one that is now true.
it("replaces the last result with the current one", async () => {
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 1);
  await press(dialog, "routingPresets.defaults.set");
  expect(dialog.textContent).toContain("routingPresets.defaults.saved");
  api.status = 500;
  await press(dialog, "routingPresets.defaults.clear");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.saved");
});

// Two people changing one preset: the change names the revision it read, so the later save is refused, the list is read
// again, and opening the preset a second time sends the revision the account now has (acceptance of #747 C5).
it("refuses to overwrite a preset changed elsewhere and reads it again", async () => {
  const { container } = await render(<RoutingPresets />);
  const edit = async () => {
    const dialog = await openEditor(container, 2);
    const form = dialog.querySelector("form") as HTMLFormElement;
    await fill(form.querySelector("input") as HTMLInputElement, "Renamed");
    await act(async () => form.requestSubmit());
    await settle();
    return dialog;
  };
  // Someone else changed the preset: the node now holds revision 2 while the page still shows revision 1.
  api.presets = api.presets.map((entry) => (entry.id === id(3) ? { ...entry, revision: 2 } : entry));
  const refused = await edit();
  expect(api.sent).toEqual([{ name: "Renamed", rules: { default: {} }, revision: 1 }]);
  expect(refused.textContent).toContain("routingPresets.form.staleHint");
  // The page read the list again: the second attempt names the revision it now has, and is accepted.
  await press(refused, "dialog.close");
  expect(document.querySelector("dialog")).toBeNull();
  await edit();
  expect(api.presetReads).toBeGreaterThan(1);
  expect(api.sent.at(-1)).toEqual({ name: "Renamed", rules: { default: {} }, revision: 2 });
  // Accepted: the dialog closed and the node holds the next revision.
  expect(document.querySelector("dialog")).toBeNull();
  expect(api.presets[2]?.revision).toBe(3);
});

// Someone who may see keys but not change them reads the preset and the default's state, and cannot change, copy,
// disable or default it.
it("offers no change to a session that may only read presets", async () => {
  api.manage = false;
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 2);
  expect(dialog.textContent).toContain("Cheapest first");
  expect(dialog.textContent).toContain("routingPresets.notice");
  expect(dialog.textContent).not.toContain("routingPresets.actions.disable");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.set");
});

// A session that loses the permission keeps what the query cache holds: the rows go and the page says why.
it("hides the presets read before the permission was taken away", async () => {
  const { client, container } = await render(<RoutingPresets />);
  await openRow(container, "routingPresets.openNamed", 2);
  expect(container.textContent).toContain("Cheapest first");
  api.view = false;
  await act(async () => {
    client.setQueryData([accountKey, "routing-presets"], { data: [...presets, preset(5, "account", "Later")] });
  });
  await settle();
  expect(container.textContent).not.toContain("Official only");
  // Nothing to make either: the permission is what the create action and the dialogs hang on, so a form cannot be opened
  // again behind a closed dialog.
  const creates = [...container.querySelectorAll("button")].filter(
    (button) => (button.getAttribute("aria-label") ?? button.textContent) === "routingPresets.create",
  );
  expect(creates).toHaveLength(0);
  expect(document.querySelector("dialog")).toBeNull();
});

// A node that refuses the read is a forbidden state, not the rows it answered before.
it("shows an old node's refusal instead of the presets it read before", async () => {
  const { client, container } = await render(<RoutingPresets />);
  expect(container.textContent).toContain("Official only");
  await act(async () => {
    client
      .getQueryCache()
      .find({ queryKey: [accountKey, "routing-presets"] })
      ?.setState({ status: "error", error: new ApiError(403, null) });
  });
  await settle();
  expect(container.textContent).not.toContain("Official only");
});

// A refused change is not the same as a read that has the new revision: the dialog says "reopen" only once that read has
// it, and says it is reading until then.
it("says a preset was changed elsewhere only after the list was read again", async () => {
  const { container } = await render(<RoutingPresets />);
  api.presets = api.presets.map((entry) => (entry.id === id(3) ? { ...entry, revision: 2 } : entry));
  api.hold = "read";
  const dialog = await openEditor(container, 2);
  const form = dialog.querySelector("form") as HTMLFormElement;
  await act(async () => form.requestSubmit());
  await settle();
  expect(dialog.textContent).toContain("routingPresets.form.staleReading");
  expect(dialog.textContent).not.toContain("routingPresets.form.staleHint");
  await act(async () => api.release?.());
  await settle();
  expect(dialog.textContent).toContain("routingPresets.form.staleHint");
});

// A read of the list that fails after the refusal offers the read again, and only then what to do.
it("offers the read again when the list could not be read after a refusal", async () => {
  const { container } = await render(<RoutingPresets />);
  api.presets = api.presets.map((entry) => (entry.id === id(3) ? { ...entry, revision: 2 } : entry));
  const editor = await openEditor(container, 2);
  const form = editor.querySelector("form") as HTMLFormElement;
  api.hold = "fail";
  await act(async () => form.requestSubmit());
  await settle();
  await act(async () => api.release?.());
  await settle();
  const dialog = document.querySelector("dialog") as HTMLElement;
  expect(dialog.textContent).toContain("routingPresets.form.staleFailed");
  await press(dialog, "routingPresets.form.retry");
  expect(dialog.textContent).toContain("routingPresets.form.staleHint");
});

// A read that fails for another reason is not a reason to throw away what the person typed: the dialog stays, the list
// offers the retry.
it("keeps a dialog open when the list read fails", async () => {
  const { client, container } = await render(<RoutingPresets />);
  await press(container, "routingPresets.create");
  const dialog = document.querySelector("dialog") as HTMLElement;
  await fill(dialog.querySelector("input") as HTMLInputElement, "Draft");
  await failRead(client, [accountKey, "routing-presets"]);
  await settle();
  expect(document.querySelector("dialog")).not.toBeNull();
  expect((document.querySelector("dialog") as HTMLElement).querySelector("input")?.value).toBe("Draft");
});
