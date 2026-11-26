import type { AdminSchemas } from "@axiom/api";

type Schedule = AdminSchemas["PriceSchedule"];
type Publication = AdminSchemas["PricePublication"];

// A unit price is exact decimal text with at most 15 decimals (api/admin.yaml UnitPrice), so two of them compare as the
// whole numbers they are in units of 10^-15. Nothing is rounded, and nothing is computed from them: this only says which
// of two prices the server gave is the lower.
const scale = 15;
const scaled = (price: string) => {
  const [whole = "0", fraction = ""] = price.split(".");
  return BigInt(`${whole}${fraction.padEnd(scale, "0")}`);
};

export const lower = (price: string, than: string) => scaled(price) < scaled(than);

// Some item of the price list is under the floor's price for it. An item the floor does not price has no floor.
function under(price: Schedule | undefined, floor: Schedule | undefined) {
  if (!price || !floor) return false;
  return Object.entries(floor.rates).some(([item, least]) => {
    const asked = price.rates[item as keyof Schedule["rates"]];
    return asked !== undefined && lower(asked, least);
  });
}

// belowFloor tells whether the price the rule asks for is under the site's floor, as the server's own `requested` and
// `floor` (and, for a proposal that prices endpoints apart, each endpoint's) say. Publishing it then lifts it to the
// floor, which `schedule` already is.
export function belowFloor(publication: Publication) {
  const { requested, floor, requested_endpoints: asked, floors } = publication;
  return (
    under(requested, floor) || Object.entries(asked ?? {}).some(([endpoint, price]) => under(price, floors?.[endpoint]))
  );
}

// raises tells whether the new price asks more than the current one for any item both price, which a publication
// tells the person before it makes it so for every user at once.
export function raises(next: Schedule | undefined, current: Schedule | undefined) {
  if (!next || !current) return false;
  return Object.entries(next.rates).some(([item, price]) => {
    const was = current.rates[item as keyof Schedule["rates"]];
    return was !== undefined && lower(was, price);
  });
}
