import type { AdminSchemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import { costShare, modelView, tokenPrice } from "./by-model";
import { categoryOf, mismatch } from "./inbox";
import { byFamily, credentialName, pastedKeys, serviceAccount, trafficOf } from "./upstream";

type Row = AdminSchemas["PricingPreviewRow"];
const schedule = (input?: string, output?: string): AdminSchemas["PriceSchedule"] => ({
  v: 1,
  currency: "USD",
  rates: { ...(input ? { input } : {}), ...(output ? { output } : {}) },
});
const row = (endpoint: string, cost: AdminSchemas["PriceSchedule"], margin?: string): Row => ({
  model_id: "m",
  slug: "a/m",
  status: "priced",
  endpoint_id: endpoint,
  cost,
  sale: schedule("0.000004", "0.000012"),
  margin_ratio: margin,
});

describe("pasted keys", () => {
  it.each([
    ["", [], 0],
    ["sk-a\nsk-b\n\nsk-a", ["sk-a", "sk-b"], 1],
    ["  sk-a   sk-b\tsk-c ", ["sk-a", "sk-b", "sk-c"], 0],
  ])("counts %j once each and the repeats", (text, keys, duplicates) => {
    expect(pastedKeys(text)).toEqual({ keys, duplicates });
  });
});

describe("service account key", () => {
  it("reads the project and the account only from a whole key", () => {
    const key = JSON.stringify({ project_id: "p", client_email: "a@p.iam", private_key: "secret" });
    expect(serviceAccount(key)).toEqual({ project: "p", email: "a@p.iam" });
    expect(serviceAccount(JSON.stringify({ project_id: "p", client_email: "a@p.iam" }))).toBeNull();
    expect(serviceAccount("{ not json")).toBeNull();
    expect(serviceAccount("null")).toBeNull();
  });
});

describe("traffic", () => {
  it("shares the calls out by supplier and line, and says nothing of a rate without calls", () => {
    const traffic = trafficOf([
      { supplier_id: "s1", group_id: "g1", calls: 300, failures: 3 },
      { supplier_id: "s1", group_id: "g2", calls: 100, failures: 1 },
      { supplier_id: "s2", group_id: "g3", calls: 0, failures: 0 },
    ]);
    expect(traffic.ofSupplier("s1")).toMatchObject({ calls: 400, share: 1, rate: 0.99 });
    expect(traffic.ofGroup("g2")).toMatchObject({ share: 0.25, rate: 0.99 });
    expect(traffic.ofSupplier("s2").rate).toBeUndefined();
    expect(traffic.ofGroup("none")).toEqual({ calls: 0, share: 0, rate: undefined, firstTokenMs: undefined });
    expect(trafficOf([]).ofSupplier("s1").share).toBe(0);
  });
});

it("names a pasted key by its last four, a region by its name and a relay key by its reference", () => {
  expect(credentialName({ last4: "abcd", upstream_key_ref: "ref" })).toBe("…abcd");
  expect(credentialName({ region: "us-east5", upstream_key_ref: "ref" })).toBe("us-east5");
  expect(credentialName({ upstream_key_ref: "key-1" })).toBe("key-1");
});

describe("by model", () => {
  it("finds the cheapest line among those that may take a request, and the sale over it", () => {
    const rows = [row("e1", schedule("0.000003", "0.000009")), row("e2", schedule("0.000001", "0.000002"), "0.5")];
    expect(modelView(rows, () => true).cheapest?.endpoint_id).toBe("e2");
    // the cheaper line is off, so the other is the cheapest that serves
    expect(modelView(rows, (id) => id !== "e2").cheapest?.endpoint_id).toBe("e1");
    expect(modelView(rows, () => false).cheapest).toBeUndefined();
    expect(modelView(rows, () => true).sale).toEqual(schedule("0.000004", "0.000012"));
  });

  it("leaves a line charged by the request out of the token comparison", () => {
    expect(tokenPrice(schedule())).toBeUndefined();
    expect(modelView([row("e1", schedule())], () => true).cheapest).toBeUndefined();
  });

  it.each([
    [schedule("0.000002", "0.000006"), 50],
    [schedule("0.000008", "0.000016"), 150],
    [schedule(), undefined],
  ])("puts a cost at its share of the sale price", (cost, share) => {
    const got = costShare(cost, schedule("0.000004", "0.000012"));
    if (share === undefined) expect(got).toBeUndefined();
    else expect(got).toBeCloseTo(share);
  });
});

describe("the queue", () => {
  it("files every kind under one of the four filters", () => {
    expect(new Set(Object.values(categoryOf))).toEqual(new Set(["price", "model", "key", "balance"]));
  });

  it.each([
    ["100", "108.2", 820],
    ["100", "99", -100],
    ["0", "5", undefined],
    [undefined, "5", undefined],
  ])("works out a day's difference from %s and %s", (estimated, charged, bp) => {
    expect(mismatch(estimated, charged)).toBe(bp);
  });
});

it("groups the known names by family, the largest first, each model once", () => {
  const name = (upstream_name: string, family?: string, model_id?: string): AdminSchemas["SupplierModel"] => ({
    group_id: "g",
    upstream_name,
    formats: ["chat"],
    pile: "known",
    first_seen_at: "2026-11-01T00:00:00Z",
    family,
    model_id,
  });
  const families = byFamily(
    [name("a", "gpt", "m1"), name("b", "claude", "m2"), name("c", "gpt", "m1"), name("d", undefined, "m3")],
    "none",
  );
  expect(families).toEqual([
    { family: "gpt", names: ["a", "c"], models: ["m1"] },
    { family: "claude", names: ["b"], models: ["m2"] },
    { family: "none", names: ["d"], models: ["m3"] },
  ]);
});
