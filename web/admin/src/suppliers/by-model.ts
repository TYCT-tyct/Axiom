import type { AdminSchemas } from "@axiom/api";

type Row = AdminSchemas["PricingPreviewRow"];
type Schedule = AdminSchemas["PriceSchedule"];

// What a schedule charges for a token in and a token out together: the one figure the view by model compares lines by
// (adUpModels' sum), meaningful only between schedules in one currency, as the preview's are. None for a schedule that
// prices neither, such as a line charged by the request.
export function tokenPrice(schedule: Schedule | undefined) {
  const input = schedule?.rates.input;
  const output = schedule?.rates.output;
  if (input === undefined && output === undefined) return undefined;
  return Number(input ?? 0) + Number(output ?? 0);
}

export type ModelView = {
  // The rows of the preview that price an endpoint of the model, each with its cost.
  endpoints: Map<string, Row>;
  // What the model sells for now, or would under the stored rules when nothing is published yet.
  sale?: Schedule;
  // The endpoint whose cost by the token is lowest, of those that may be called.
  cheapest?: Row;
};

// modelView reads a model's preview rows (previewPricing: a row per endpoint, with its cost and the sale over it): the
// sale price, and the cheapest of the endpoints that may take a request now.
export function modelView(rows: readonly Row[], callable: (endpointId: string) => boolean): ModelView {
  const endpoints = new Map(rows.flatMap((row) => (row.endpoint_id ? [[row.endpoint_id, row] as const] : [])));
  const priced = [...endpoints.values()].filter(
    (row) => row.endpoint_id && callable(row.endpoint_id) && tokenPrice(row.cost) !== undefined,
  );
  const cheapest = priced.reduce<Row | undefined>(
    (best, row) => (best && (tokenPrice(best.cost) ?? 0) <= (tokenPrice(row.cost) ?? 0) ? best : row),
    undefined,
  );
  const sale = rows.map((row) => row.current_sale ?? row.sale).find((schedule) => schedule !== undefined);
  return { endpoints, sale, cheapest };
}

// How much of the sale price a line's cost takes, in percent; above 100 the line loses money on every token.
export function costShare(cost: Schedule | undefined, sale: Schedule | undefined) {
  const paid = tokenPrice(cost);
  const sold = tokenPrice(sale);
  return paid === undefined || !sold ? undefined : (paid / sold) * 100;
}
