import type { AdminSchemas } from "@axiom/api";
import { isProblem, type Problem } from "../change";
import { type Invalid, readSchedule } from "../models/price-fields";
import { modeOf } from "./upstream";

type Schedule = AdminSchemas["PriceSchedule"];

// A model of a group whose supplier states no price for it, by the name the group lists it under: what a base price is
// confirmed for (api/admin.yaml BaseModel).
export type BaseModel = { group_id: string; upstream_name: string };

// What the confirm dialog is opened for: the name, and the catalog model it maps to now, read from the supplier's
// listing (listSupplierModels), whose official price is the default and which the server confirms for.
export type BaseTarget = BaseModel & { model_id: string; model_slug?: string };

// Where the base comes from: the catalog's official price in the settlement currency, which the server reads itself, or
// the prices a person typed item by item.
export type BaseChoice = { kind: "official" } | { kind: "entered"; price: Schedule };

// The kinds whose adapter never takes a price the supplier publishes as a cost (docs/domains/suppliers.md「sub2api」
// 「CPA」), so a cost that is unknown there is one only a confirmed base can give. A new-api supplier may publish a
// price the server keeps and cannot express, which a base would never replace, and the contract does not yet say which
// cost is unknown for which reason, so a new-api supplier is not offered a base here.
const unpricedKinds: ReadonlySet<string> = new Set(["sub2api", "cpa"]);

export function takesBases(supplier: AdminSchemas["Supplier"]) {
  return modeOf(supplier) === "relay" && unpricedKinds.has(supplier.kind);
}

// The listed name an endpoint can be given a base from: the one its group lists under the endpoint's name now, mapped to
// the endpoint's own model. An endpoint whose name maps to another model since is not offered one: the server would
// confirm the base for the other model.
export function targetOf(
  endpoint: AdminSchemas["SupplierEndpoint"],
  listed: (model: BaseModel) => AdminSchemas["SupplierModel"] | undefined,
): BaseTarget | undefined {
  if (endpoint.cost_state !== "unknown") return undefined;
  const row = listed(endpoint);
  return row?.model_id === endpoint.model_id ? nameTarget(row) : undefined;
}

// A listed name as a target: only a name mapped to a model can be given a base.
export function nameTarget(row: AdminSchemas["SupplierModel"]): BaseTarget | undefined {
  return row.model_id
    ? { group_id: row.group_id, upstream_name: row.upstream_name, model_id: row.model_id, model_slug: row.model_slug }
    : undefined;
}

// The catalog's current price of the model in the currency: the whole price list, tiers and all, which is what the
// server takes as the base when no price is given.
export function officialIn(model: AdminSchemas["Model"], currency: string): Schedule | undefined {
  return model.prices.find((price) => price.currency === currency)?.schedule;
}

// The choice the form holds; the entered prices are in the settlement currency, whatever the form was filled from.
export function readBase(values: Record<string, string>, currency: string, invalid: Invalid): BaseChoice | Problem {
  if (values.source === "official") {
    return { kind: "official" };
  }
  const price = readSchedule(values, { v: 1, currency, rates: {} }, invalid);
  return isProblem(price) ? price : { kind: "entered", price };
}

// The request confirmSupplierBasePrices takes for one model: no price means the official one.
export function confirmation(model: BaseModel, choice: BaseChoice, reason: string) {
  const item = { group_id: model.group_id, upstream_name: model.upstream_name };
  return {
    items: [choice.kind === "entered" ? { ...item, price: choice.price } : item],
    reason,
  } as AdminSchemas["BasePriceConfirmation"];
}
