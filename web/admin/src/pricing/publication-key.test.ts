import { expect, it } from "vitest";
import { publicationKey } from "./publication-key";

it("keeps recovery identity across candidate and head changes", () => {
  const price = { model_id: "model", audience: { kind: "account" as const, id: "acct" } };
  expect(publicationKey({ ...price, candidate_id: "old", current_id: "head" })).toBe(
    publicationKey({ ...price, candidate_id: "new", current_id: "next" }),
  );
});

it("isolates model and audience confirmations and retains old-node default identity", () => {
  const all = { model_id: "model", audience: { kind: "all" as const } };
  const tier = { model_id: "model", audience: { kind: "tier" as const, id: "tier" } };
  expect(
    new Set([publicationKey(all), publicationKey(tier), publicationKey({ ...tier, model_id: "other" })]).size,
  ).toBe(3);
  expect(publicationKey({ model_id: "model" })).toBe(publicationKey(all));
  expect(publicationKey(all)).toBe("model");
});
