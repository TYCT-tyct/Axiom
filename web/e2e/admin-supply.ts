import type { AdminState, Schemas } from "./admin-data.ts";
import { aSupplier, uuid } from "./admin-data.ts";
import { base, type Ctx, changes, reads, route, safe } from "./admin-route.ts";
import { fail, type Handler, ok, paged } from "./mock.ts";

// Suppliers and the upstream model names waiting to be mapped. A supplier that is connecting settles after it has been
// read once, as the sync would have worked through it by the next poll; one that is being removed ends removed.

const find = (state: AdminState, id: string | undefined) => state.suppliers.find((s) => s.supplier.id === id);

function listSuppliers({ query, state }: Ctx<"/api/admin/v1/suppliers", "get">) {
  const status = query.get("status");
  const rows = state.suppliers.map((detail) => detail.supplier).filter((s) => !status || s.status === status);
  const page = paged(rows, query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function settle(state: AdminState, detail: Schemas["SupplierDetail"]) {
  const id = detail.supplier.id;
  const reads = (state.reads.get(id) ?? 0) + 1;
  state.reads.set(id, reads);
  if (reads < 2) {
    return;
  }
  if (detail.supplier.status === "connecting") {
    detail.supplier.status = "active";
    detail.groups = [
      { id: uuid(3, reads), upstream_ref: "default", name: "default", ratio: "1", selected: false, present: true },
      { id: uuid(3, reads + 1), upstream_ref: "vip", name: "vip", ratio: "1.5", selected: false, present: true },
    ];
    detail.unbound_keys = [{ ref: "key-new", group_ref: "default", status: "active" }];
  }
}

function getSupplier({ params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}", "get">) {
  const detail = find(state, params.supplier_id);
  if (!detail) {
    return fail(404, "not_found");
  }
  if (detail.supplier.status === "connecting") {
    settle(state, detail);
  }
  return ok(detail);
}

// A probe by address: the host decides the answer, so a test picks the case it wants.
function detect({ body }: Ctx<"/api/admin/v1/suppliers/detections", "post">) {
  const origin = body.origin.toLowerCase();
  if (origin.includes("unknown")) {
    return ok({ origin, login_fields: [] });
  }
  if (origin.includes("sub2")) {
    return ok({
      origin,
      kind: "sub2api",
      generation: "1.0",
      detected_version: "0.9.1",
      login_fields: ["access_token", "refresh_token", "access_token_expires_at"],
    } satisfies Schemas["Detection"]);
  }
  return ok({
    origin,
    kind: "newapi",
    generation: "1.0",
    detected_version: "0.8.3",
    login_fields: ["access_token", "access_token_expires_at"],
  } satisfies Schemas["Detection"]);
}

function createSupplier({ body, state }: Ctx<"/api/admin/v1/suppliers", "post">) {
  if (
    state.suppliers.some(
      (d) => d.supplier.origin === body.origin && d.supplier.upstream_account === body.upstream_account,
    )
  ) {
    return fail(409, "conflict");
  }
  const supplier = aSupplier(state.suppliers.length + 1, {
    name: body.name,
    kind: body.kind,
    origin: body.origin,
    upstream_account: body.upstream_account,
    settlement_currency: body.settlement_currency,
    status: "connecting",
    balance_native: undefined,
    unit_value: undefined,
    unit_value_hint: undefined,
  });
  state.suppliers.unshift({ supplier, groups: [], credentials: [], unbound_keys: [], endpoints: [] });
  return { status: 201, json: supplier } as const;
}

function setStatus({ body, params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/status", "put">) {
  const detail = find(state, params.supplier_id);
  if (!detail) {
    return fail(404, "not_found");
  }
  const { status } = detail.supplier;
  if (!["active", "low_balance", "paused"].includes(status)) {
    return fail(409, "conflict");
  }
  detail.supplier.status = body.status;
  return ok(detail.supplier);
}

function remove({ params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/removal", "post">) {
  const detail = find(state, params.supplier_id);
  if (!detail) {
    return fail(404, "not_found");
  }
  detail.supplier.status = "removed";
  for (const credential of detail.credentials) {
    credential.status = "erased";
  }
  return { status: 200, json: detail.supplier } as const;
}

function replaceLogin({ params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/login", "put">) {
  const detail = find(state, params.supplier_id);
  if (!detail) {
    return fail(404, "not_found");
  }
  detail.supplier.status = "connecting";
  detail.supplier.status_reason = undefined;
  state.reads.set(detail.supplier.id, 0);
  return ok(detail.supplier);
}

function setUnitValue({ body, params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/unit-value", "put">) {
  const detail = find(state, params.supplier_id);
  if (!detail) {
    return fail(404, "not_found");
  }
  detail.supplier.unit_value = body.value;
  return ok(detail.supplier);
}

function selectGroup({ body, params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/groups/{group_id}", "put">) {
  const group = find(state, params.supplier_id)?.groups.find((g) => g.id === params.group_id);
  if (!group) {
    return fail(404, "not_found");
  }
  group.selected = body.selected;
  if (body.selected && !group.codename) {
    group.codename = `Code ${group.name}`;
  }
  return ok(group);
}

function bindKey({ body, params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/credentials", "post">) {
  const detail = find(state, params.supplier_id);
  const key = detail?.unbound_keys.find((k) => k.ref === body.upstream_key_ref);
  if (!detail || !key) {
    return fail(404, "not_found");
  }
  detail.unbound_keys = detail.unbound_keys.filter((k) => k !== key);
  const credential = {
    id: uuid(4, detail.credentials.length + 50),
    group_id: body.group_id,
    upstream_key_ref: key.ref,
    status: "binding",
    max_concurrency: 4,
  } as const;
  detail.credentials.push(credential);
  return { status: 201, json: credential } as const;
}

type Finding = Pick<Schemas["EndpointTest"], "outcome" | "problem" | "upstream_status" | "first_token_ms">;
const findings: Record<string, Finding> = {
  "broken-stream-model": {
    outcome: "failed",
    problem: { cause: "stream_broken", scope: "endpoint" },
    upstream_status: 200,
    first_token_ms: 640,
  },
  "rate-limited-model": {
    outcome: "inconclusive",
    problem: { cause: "rate_limited", scope: "credential" },
    upstream_status: 429,
  },
};

// A test of an endpoint: what it finds follows the endpoint's upstream name (admin-data.ts), and one that could not be
// called is refused as the server refuses it, before anything is paid for.
function testEndpoint({ body, state }: Ctx<"/api/admin/v1/suppliers/endpoint-tests", "post">) {
  const detail = state.suppliers.find((d) => d.endpoints.some((e) => e.id === body.endpoint_id));
  const endpoint = detail?.endpoints.find((e) => e.id === body.endpoint_id);
  if (!detail || !endpoint?.enabled || endpoint.cost_state !== "known") {
    return fail(409, "conflict");
  }
  if (!endpoint.formats.includes(body.format)) {
    return fail(422, "unprocessable");
  }
  const found = findings[endpoint.upstream_name] ?? { outcome: "passed", upstream_status: 200, first_token_ms: 820 };
  return {
    status: 201,
    json: {
      request_id: uuid(10, state.stored.size + 1),
      endpoint_id: endpoint.id,
      model_id: endpoint.model_id,
      upstream_model: endpoint.upstream_name,
      format: body.format,
      total_ms: 3_240,
      upstream_request_id: found.outcome === "passed" ? "chatcmpl-9fX2kQ" : undefined,
      estimated_native: detail.supplier.native_unit === "usd" ? "0.000184" : "92",
      native_unit: detail.supplier.native_unit,
      ...found,
    } satisfies Schemas["EndpointTest"],
  } as const;
}

function listPending({ query, state }: Ctx<"/api/admin/v1/pending-mappings", "get">) {
  const supplier = query.get("supplier_id");
  const page = paged(
    state.pending.filter((row) => !supplier || row.supplier_id === supplier),
    query,
  );
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function mapModel({ body, params, state }: Ctx<"/api/admin/v1/suppliers/{supplier_id}/model-mappings", "post">) {
  const model = state.models.find((m) => m.id === body.model_id);
  if (!model) {
    return fail(404, "not_found");
  }
  if (state.pending.every((row) => row.upstream_name !== body.upstream_name)) {
    return fail(409, "conflict");
  }
  state.pending = state.pending.filter(
    (row) => !(row.supplier_id === params.supplier_id && row.upstream_name === body.upstream_name),
  );
  return {
    status: 201,
    json: {
      id: uuid(8, state.pending.length),
      supplier_id: params.supplier_id as Schemas["Uuid"],
      group_id: body.group_id,
      upstream_name: body.upstream_name,
      model_id: body.model_id,
    },
  } as const;
}

export const supplyHandlers: Handler<AdminState>[] = [
  route("get", `${base}/suppliers`, reads("suppliers.view"), listSuppliers),
  route("post", `${base}/suppliers`, changes("suppliers.manage"), createSupplier),
  route("post", `${base}/suppliers/detections`, safe("suppliers.manage"), detect),
  route("get", `${base}/suppliers/{supplier_id}`, reads("suppliers.view"), getSupplier),
  route("put", `${base}/suppliers/{supplier_id}/status`, changes("suppliers.manage"), setStatus),
  route("post", `${base}/suppliers/{supplier_id}/removal`, changes("suppliers.manage"), remove),
  route("put", `${base}/suppliers/{supplier_id}/login`, changes("suppliers.manage"), replaceLogin),
  route("put", `${base}/suppliers/{supplier_id}/unit-value`, changes("suppliers.manage"), setUnitValue),
  route("put", `${base}/suppliers/{supplier_id}/groups/{group_id}`, changes("suppliers.manage"), selectGroup),
  route("post", `${base}/suppliers/{supplier_id}/credentials`, changes("suppliers.manage"), bindKey),
  route("post", `${base}/suppliers/endpoint-tests`, changes("suppliers.manage"), testEndpoint),
  route("get", `${base}/pending-mappings`, reads("suppliers.view"), listPending),
  route("post", `${base}/suppliers/{supplier_id}/model-mappings`, changes("suppliers.manage"), mapModel),
];
