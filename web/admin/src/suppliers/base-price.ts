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

// Only a relay is synced, and so only a relay's cost can be unknown for the reason a base answers.
export function takesBases(supplier: AdminSchemas["Supplier"]) {
  return modeOf(supplier) === "relay";
}

// The listed name an endpoint can be given a base from: the one its group lists under the endpoint's name now, mapped to
// the endpoint's own model, while the supplier publishes no price for it (cost_reason unpriced). An endpoint whose name
// maps to another model since is not offered one: the server would confirm the base for the other model.
export function targetOf(
  endpoint: AdminSchemas["SupplierEndpoint"],
  listed: (model: BaseModel) => AdminSchemas["SupplierModel"] | undefined,
): BaseTarget | undefined {
  if (endpoint.cost_reason !== "unpriced") return undefined;
  const row = listed(endpoint);
  return row?.model_id === endpoint.model_id ? nameTarget(row) : undefined;
}

// A listed name as a target: a name mapped to a model whose supplier publishes no price for it. A published price the
// table cannot express, or a base past the limit, is not one a base answers.
export function nameTarget(row: AdminSchemas["SupplierModel"]): BaseTarget | undefined {
  return row.model_id && row.cost_reason === "unpriced"
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

// The request confirmSupplierBasePrices takes for one model: no price means the official one. It names the model the
// dialog showed, so a name mapped to another since is refused rather than priced for a model nobody looked at.
export function confirmation(target: BaseTarget, choice: BaseChoice, reason: string) {
  const item = { group_id: target.group_id, upstream_name: target.upstream_name, model_id: target.model_id };
  return {
    items: [choice.kind === "entered" ? { ...item, price: choice.price } : item],
    reason,
  } as AdminSchemas["BasePriceConfirmation"];
}
