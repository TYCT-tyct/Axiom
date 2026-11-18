// @vitest-environment jsdom
import type { Schemas } from "@axiom/api";
import { act } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import RoutingPresets from "./pages/routing-presets";
import { openRow, press, render, settle, setUpBoardPages } from "./testing";

type Preset = Schemas["RoutingPreset"];

// What the mocked console answers: the presets, the account's default, and what a write to the default came back with.
const api = vi.hoisted(() => ({
  sent: [] as unknown[],
  status: 200,
  reads: 0,
  manage: true,
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
  preset(3, "account", "Cheapest first", 2),
];

vi.mock("@axiom/api", async (importOriginal) => {
  const { answer, mockClient } = await import("./testing");
  return mockClient(importOriginal, {
    GET: (path: string) => {
      if (path === "/api/console/v1/routing-presets") {
        return answer(200, { data: presets });
      }
      if (path !== "/api/console/v1/routing-default") {
        return answer(503, "unavailable");
      }
      api.reads += 1;
      return answer(200, { preset_id: id(3), version: 2, new_keys_preset_id: id(3) });
    },
    POST: () =>
      answer(200, { summary: { sort: "price", verified_only: false, priced: false, overrides: 0 }, unmatched: [] }),
    PUT: (path: string, options?: { body?: unknown }) => {
      api.sent.push(options?.body);
      // A preset's own change names the revision it read, which a change made elsewhere meanwhile moves on.
      if (path.startsWith("/api/console/v1/routing-presets/")) return answer(409, "stale_revision");
      return api.status === 200
        ? answer(200, { preset_id: id(2), version: 3, new_keys_preset_id: id(2) })
        : answer(409, "stale_revision");
    },
  });
});

vi.mock("@axiom/app", async (importOriginal) => {
  const session = await (await import("./testing")).everyPermission(importOriginal);
  return { ...session, can: (_session: unknown, permission: string) => permission !== "api_keys.manage" || api.manage };
});

setUpBoardPages();

beforeEach(() => {
  api.sent = [];
  api.status = 200;
  api.reads = 0;
  api.manage = true;
});

// Each preset is one row of the board's table: its name and slug, the layer it comes from, its rules in a line, the
// account's keys bound to it and its rules' version.
it("lists the presets with their layer, rules, keys and version, and marks the default", async () => {
  const { container } = await render(<RoutingPresets />);
  const rows = [...container.querySelectorAll("tbody tr")];
  expect(rows).toHaveLength(3);
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

// A row's name opens the preset: the actions the board's table cannot draw live there, and a key's default is one
// choice per account rather than a switch per row.
it("opens a preset from its row and sets the account's default from there", async () => {
  const { container } = await render(<RoutingPresets />);
  await press(container, "routingPresets.openNamed");
  const dialog = document.querySelector("dialog");
  expect(dialog?.textContent).toContain("Official only");
  await press(dialog as HTMLElement, "routingPresets.defaults.set");
  expect(api.sent).toEqual([{ preset_id: id(1), version: 2 }]);
});

// Two people changing the default: the version the page read went on, so the save is refused, the page says so, and it
// reads the default again — the next press sends the version the account has now instead of overwriting the other's.
it("refuses a stale default and reloads it", async () => {
  api.status = 409;
  const { container } = await render(<RoutingPresets />);
  await press(container, "routingPresets.openNamed");
  const dialog = document.querySelector("dialog") as HTMLElement;
  await press(dialog, "routingPresets.defaults.set");
  expect(dialog.textContent).toContain("routingPresets.defaults.reloaded");
  expect(api.reads).toBeGreaterThan(1);
});

// Two people changing one preset: the change names the revision it read, so the later save is refused, and the dialog
// says the preset was just changed elsewhere rather than overwriting it (acceptance of #747 C5).
it("refuses to overwrite a preset changed elsewhere", async () => {
  const { container } = await render(<RoutingPresets />);
  await openRow(container, "routingPresets.openNamed", 2);
  await press(document.querySelector("dialog") as HTMLElement, "routingPresets.actions.edit");
  const dialog = document.querySelector("dialog") as HTMLElement;
  const form = dialog.querySelector("form") as HTMLFormElement;
  await act(async () => {
    const input = form.querySelector("input") as HTMLInputElement;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, "Renamed");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => form.requestSubmit());
  await settle();
  // The change names the revision the preset was read at (1) and sends its rules back whole.
  expect(api.sent).toEqual([{ name: "Renamed", rules: { default: {} }, revision: 1 }]);
  expect(dialog.textContent).toContain("routingPresets.form.staleHint");
});

// Someone who may see keys but not change them reads the preset and cannot change, copy, disable or default it.
it("offers no change to a session that may only read presets", async () => {
  api.manage = false;
  const { container } = await render(<RoutingPresets />);
  const dialog = await openRow(container, "routingPresets.openNamed", 2);
  expect(dialog.textContent).toContain("Cheapest first");
  expect(dialog.textContent).toContain("routingPresets.notice");
  expect(dialog.textContent).not.toContain("routingPresets.actions.disable");
  expect(dialog.textContent).not.toContain("routingPresets.defaults.set");
});
