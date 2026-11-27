import type { AdminSchemas } from "@axiom/api";
import { isProblem, type Problem } from "../change";
import { type Invalid, readSchedule } from "../models/price-fields";

type Schedule = AdminSchemas["PriceSchedule"];
type Rates = AdminSchemas["Rates"];

// A model of a group whose supplier states no price for it, by the name the group lists it under: what a base price is
// confirmed for (api/admin.yaml BaseModel).
export type BaseModel = { group_id: string; upstream_name: string };

// Where the base comes from: the catalog's official price in the settlement currency, which the server reads itself, or
// the prices a person typed item by item.
export type BaseChoice = { kind: "official" } | { kind: "entered"; price: Schedule };

// The catalog's current price of the model in the currency, as a plain price list: a base prices the standard items, so
// tiers and service tiers are not carried into a form that edits only those.
export function officialIn(model: AdminSchemas["Model"], currency: string): Schedule | undefined {
  const schedule = model.prices.find((price) => price.currency === currency)?.schedule;
  return schedule ? { v: 1, currency, rates: schedule.rates } : undefined;
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
  const item = choice.kind === "entered" ? { ...model, price: choice.price } : model;
  return { items: [item], reason } as AdminSchemas["BasePriceConfirmation"];
}

// a × b as exact decimal text: a preview of a cost must not move by the browser's rounding.
function times(a: string, b: string) {
  const [aw = "0", af = ""] = a.split(".");
  const [bw = "0", bf = ""] = b.split(".");
  const places = af.length + bf.length;
  const digits = (BigInt(aw + af) * BigInt(bw + bf)).toString().padStart(places + 1, "0");
  const whole = digits.slice(0, digits.length - places);
  const fraction = digits.slice(digits.length - places).replace(/0+$/, "");
  return fraction ? `${whole}.${fraction}` : whole;
}

// What a base costs in a group: each price times the group's ratio. The server rounds the product up once in the
// supplier's unit, so this is the cost to within that rounding.
export function scaled(schedule: Schedule, ratio: string): Schedule {
  const rates = Object.fromEntries(
    Object.entries(schedule.rates).map(([item, price]) => [item, times(price as string, ratio)]),
  ) as Rates;
  return { ...schedule, rates };
}
