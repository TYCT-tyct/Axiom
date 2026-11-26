import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { linesOf } from "./lines";

type Supplier = AdminSchemas["Supplier"];
type Group = AdminSchemas["SupplierGroup"];

const supplier = (name: string, status: Supplier["status"] = "active"): Supplier => ({
  id: name,
  name,
  kind: "newapi",
  origin: "https://example.com",
  upstream_account: "ops",
  native_unit: "usd",
  settlement_currency: "USD",
  status,
});

const group = (name: string, rest: Partial<Group> = {}): Group => ({
  id: `${name}-id`,
  upstream_ref: name,
  name,
  ratio: "1",
  selected: true,
  present: true,
  ...rest,
});

it("names a supplier's only line by the supplier, and a supplier with several by supplier and group", () => {
  const lines = linesOf([
    { supplier: supplier("Harbor"), groups: [group("default", { codename: "h1" })] },
    { supplier: supplier("North"), groups: [group("default"), group("vip"), group("idle", { selected: false })] },
  ]);
  expect(lines.map((line) => line.name)).toEqual(["Harbor", "North/default", "North/vip"]);
  expect(lines[0]?.codename).toBe("h1");
});

it.each([
  ["a supplier that syncs and a key that works", supplier("a"), group("g", { key_state: "ready" }), true],
  ["a supplier low on balance still syncs", supplier("a", "low_balance"), group("g"), true],
  ["a paused supplier", supplier("a", "paused"), group("g"), false],
  ["a group the supplier no longer lists", supplier("a"), group("g", { present: false }), false],
  ["a group whose key failed", supplier("a"), group("g", { key_state: "failed" }), false],
  [
    "a group that needs no key because it cannot be called",
    supplier("a"),
    group("g", { key_state: "unavailable" }),
    false,
  ],
])("takes a request through %s: %s", (_, from, of, usable) => {
  expect(linesOf([{ supplier: from, groups: [of] }])[0]?.usable).toBe(usable);
});
