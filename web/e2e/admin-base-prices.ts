import type { AdminState, Schemas } from "./admin-data.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { fail, type Handler, ok, paged } from "./mock.ts";

// A supplier's listed names and its base prices (confirmSupplierBasePrices), with the price lines they make.

const find = (state: AdminState, id: string | undefined) => state.suppliers.find((s) => s.supplier.id === id);

// listSupplierModels for the listed pile only: each endpoint's name, mapped to the endpoint's model, as the server lists
// a name on sale. The other piles answer as a node without the read did before, so the pages that show them are as
// they were.
function listSupplierModels({ params, query, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/models", "get">) {
  const detail = find(state, params.supplier_id);
  if (!detail || query.get("pile") !== "listed") {
    return fail(404, "not_found");
  }
  const rows = detail.endpoints.map((e) => ({
    group_id: e.group_id,
    upstream_name: e.upstream_name,
    formats: e.formats,
    pile: "listed" as const,
    first_seen_at: "2026-11-20T08:00:00Z",
    model_id: e.model_id,
    model_slug: state.models.find((m) => m.id === e.model_id)?.slug,
    endpoint_id: e.id,
    cost_reason: e.cost_reason,
  }));
  const page = paged(rows, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

// Base prices a person confirmed, by group and upstream name, in the settlement currency. The mock prices the endpoint at
// once, where the server makes the sync's next round due and that round prices it; the page shows the same either way.
const bases = new WeakMap<AdminState, Map<string, Schemas["PriceSchedule"]>>();
type Named = { group_id: string; upstream_name: string };
const baseKey = (item: Named) => `${item.group_id}/${item.upstream_name}`;
function basesOf(state: AdminState) {
  const found = bases.get(state) ?? new Map<string, Schemas["PriceSchedule"]>();
  bases.set(state, found);
  return found;
}
const endpointOf = (detail: Schemas["SupplierDetail"], item: Named) =>
  detail.endpoints.find((e) => e.group_id === item.group_id && e.upstream_name === item.upstream_name);

function confirmBases({ body, params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/base-prices", "post">) {
  const detail = find(state, params.supplier_id);
  if (!detail) {
    return fail(404, "not_found");
  }
  if (detail.supplier.unit_value === undefined) {
    return fail(409, "conflict");
  }
  const currency = detail.supplier.settlement_currency;
  const data: Schemas["BasePriceList"]["data"] = [];
  for (const item of body.items) {
    const endpoint = endpointOf(detail, item);
    const model = state.models.find((m) => m.id === endpoint?.model_id);
    const price = item.price ?? model?.prices.find((p) => p.currency === currency)?.schedule;
    if (!endpoint || !price) {
      return fail(422, "unprocessable");
    }
    if (item.model_id !== undefined && item.model_id !== endpoint.model_id) {
      return fail(409, "stale_revision");
    }
    basesOf(state).set(baseKey(item), price);
    endpoint.cost_state = "known";
    delete endpoint.cost_reason;
    data.push({ group_id: item.group_id, upstream_name: item.upstream_name, base: { ...price, currency: "XXX" } });
  }
  return ok({ data });
}

function clearBases({ body, params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/base-price-removals", "post">) {
  const detail = find(state, params.supplier_id);
  if (!detail) {
    return fail(404, "not_found");
  }
  const data: Schemas["BasePriceList"]["data"] = [];
  for (const item of body.items) {
    const base = basesOf(state).get(baseKey(item));
    const endpoint = endpointOf(detail, item);
    if (base && endpoint) {
      basesOf(state).delete(baseKey(item));
      endpoint.cost_state = "unknown";
      endpoint.cost_reason = "unpriced";
      data.push({ ...item, base: { ...base, currency: "XXX" } });
    }
  }
  return ok({ data });
}

// An endpoint's price lines, for one priced by a confirmed base (the base times the group's ratio) or whose cost is
// unknown; any other answers as a node without the read did before.
function endpointCost({
  params,
  state,
}: Ctx<"/api/admin/v1/suppliers/{supplier_id}/endpoints/{endpoint_id}/cost", "get">) {
  const detail = find(state, params.supplier_id);
  const endpoint = detail?.endpoints.find((e) => e.id === params.endpoint_id);
  if (!detail || !endpoint) {
    return fail(404, "not_found");
  }
  const settlement_currency = detail.supplier.settlement_currency;
  const base = basesOf(state).get(baseKey(endpoint));
  if (endpoint.cost_state === "unknown") {
    return ok({ endpoint_id: endpoint.id, cost_state: "unknown", settlement_currency, lines: [] } as Cost);
  }
  if (!base) {
    return fail(404, "not_found");
  }
  const ratio = detail.groups.find((g) => g.id === endpoint.group_id)?.ratio ?? "1";
  const lines = Object.entries(base.rates).map(([item, price]) => ({
    item: item as Schemas["MeteringItem"],
    unit: "token" as const,
    price: String(Number(price) * Number(ratio)),
    factors: [
      { kind: "base_price" as const, value: String(price) },
      { kind: "group_ratio" as const, value: ratio },
    ],
  }));
  return ok({ endpoint_id: endpoint.id, cost_state: "known", source: "confirmed", settlement_currency, lines } as Cost);
}
type Cost = Schemas["EndpointCost"];

export const basePriceHandlers: Handler<AdminState>[] = [
  route("get", `${base}/suppliers/{supplier_id}/models`, reads("suppliers.view"), listSupplierModels),
  route("post", `${base}/suppliers/{supplier_id}/base-prices`, changes("suppliers.manage"), confirmBases),
  route("post", `${base}/suppliers/{supplier_id}/base-price-removals`, changes("suppliers.manage"), clearBases),
  route("get", `${base}/suppliers/{supplier_id}/endpoints/{endpoint_id}/cost`, reads("suppliers.view"), endpointCost),
];
