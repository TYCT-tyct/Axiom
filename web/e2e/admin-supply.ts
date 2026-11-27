import { basePriceHandlers } from "./admin-base-prices.ts";
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

type SupplierType = Schemas["SupplierType"];
const keysForm = [{ name: "api_key", fields: [{ name: "keys", kind: "keys", required: true }] }] as const;
const aType = (kind: string, name: string, over: Partial<SupplierType> = {}): SupplierType => ({
  kind,
  mode: "direct",
  name,
  group: "official",
  entries: [],
  families: ["openai"],
  forms: [...keysForm.map((form) => ({ ...form, fields: [...form.fields] }))],
  fields: [],
  lists_models: true,
  reads_balance: false,
  ...over,
});

// The channel types of listSupplierTypes: a relay, direct APIs with and without price entries (one that sells nothing
// yet, greyed), and a cloud platform with its own fields.
const supplierTypes: SupplierType[] = [
  aType("openai", "OpenAI", {
    vendor: "openai",
    entries: [{ id: "global", origin: "https://api.openai.com", currency: "USD", models: 12 }],
  }),
  aType("stepfun", "StepFun", {
    group: "domestic",
    vendor: "stepfun",
    entries: [{ id: "cn", origin: "https://api.stepfun.com", currency: "CNY", models: 0 }],
  }),
  aType("moonshot", "Moonshot", {
    group: "domestic",
    vendor: "moonshot",
    families: ["openai", "anthropic"],
    entries: [
      { id: "cn", origin: "https://api.moonshot.cn", currency: "CNY", models: 6 },
      { id: "global", origin: "https://api.moonshot.ai", currency: "USD", models: 5 },
    ],
  }),
  aType("compatible", "Custom API", { group: "domestic", families: ["openai", "anthropic"] }),
  aType("azure", "Azure", {
    mode: "cloud",
    group: "cloud",
    vendor: "azure",
    forms: [{ name: "api_key", fields: [{ name: "api_key", kind: "secret", required: true }] }],
    fields: [
      { name: "api_version", kind: "text", required: false },
      { name: "deployments", kind: "deployments", required: true },
    ],
  }),
  aType("newapi", "New API", { mode: "relay", group: "relay", forms: [], lists_models: false }),
];

function listSupplierTypes() {
  return ok({ data: supplierTypes });
}

// A direct API or a cloud platform connected: every key (or the one credential) passes its check.
function createTypedSupplier({ body, params, state }: Ctx<"/api/admin/v1/supplier-types/{kind}/suppliers", "post">) {
  const type = supplierTypes.find((t) => t.kind === params.kind && t.mode !== "relay");
  if (!type) {
    return fail(404, "not_found");
  }
  const supplier = aSupplier(state.suppliers.length + 1, {
    name: body.name,
    kind: type.kind,
    mode: type.mode,
    origin: body.origin ?? type.entries[0]?.origin ?? "https://api.example.com",
    settlement_currency: body.settlement_currency ?? type.entries[0]?.currency ?? "USD",
    status: "active",
  });
  state.suppliers.unshift({ supplier, groups: [], credentials: [], unbound_keys: [], endpoints: [] });
  const checks = (body.keys ?? [""]).map((key, index) => ({ index, last4: key.slice(-4), outcome: "passed" as const }));
  return { status: 201, json: { supplier, checks } } as const;
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

function setEndpointEnabled({
  body,
  params,
  state,
}: Ctx<"/api/admin/v1/suppliers/{supplier_id}/endpoints/{endpoint_id}/enabled", "put">) {
  const endpoint = find(state, params.supplier_id)?.endpoints.find((e) => e.id === params.endpoint_id);
  if (!endpoint) {
    return fail(404, "not_found");
  }
  endpoint.enabled = body.enabled;
  return ok(endpoint);
}

function setCredentialLimits({
  body,
  params,
  state,
}: Ctx<"/api/admin/v1/suppliers/{supplier_id}/credentials/{credential_id}/limits", "put">) {
  const credential = find(state, params.supplier_id)?.credentials.find((c) => c.id === params.credential_id);
  if (!credential) {
    return fail(404, "not_found");
  }
  if (!Number.isInteger(body.max_concurrency) || body.max_concurrency < 1) {
    return fail(400, "invalid_request", { "/max_concurrency": "must be at least 1" });
  }
  if (
    body.requests_per_minute !== undefined &&
    body.requests_per_minute !== null &&
    (!Number.isInteger(body.requests_per_minute) || body.requests_per_minute < 1 || body.requests_per_minute > 100_000)
  ) {
    return fail(400, "invalid_request", { "/requests_per_minute": "must be between 1 and 100000" });
  }
  credential.max_concurrency = body.max_concurrency;
  if (body.requests_per_minute === undefined || body.requests_per_minute === null) {
    delete credential.requests_per_minute;
  } else {
    credential.requests_per_minute = body.requests_per_minute;
  }
  return ok(credential);
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

// 上游's queue, as far as the mock has it: each name waiting to be mapped is an unknown_model item, with an ID that
// stays the item's while the queue changes around it; an ignored one is hidden until the state is made again.
const ignored = new WeakMap<AdminState, Set<string>>();
const itemId = (row: Schemas["PendingMapping"]) =>
  uuid(
    12,
    [...`${row.group_id}/${row.upstream_name}`].reduce((sum, c, at) => (sum * 31 + c.charCodeAt(0) + at) % 1e9, 7),
  );

function inboxOf(state: AdminState): Schemas["SupplierInboxItem"][] {
  const hidden = ignored.get(state) ?? new Set<string>();
  return state.pending
    .map((row) => ({
      id: itemId(row),
      kind: "unknown_model" as const,
      supplier_id: row.supplier_id,
      group_id: row.group_id,
      upstream_name: row.upstream_name,
      first_seen_at: "2026-11-20T08:00:00Z",
      last_seen_at: "2026-11-25T08:00:00Z",
    }))
    .filter((item) => !hidden.has(item.id));
}

function listInbox({ query, state }: Ctx<"/api/admin/v1/supplier-inbox", "get">) {
  const page = paged(inboxOf(state), query);
  return page ? ({ status: 200, json: page } as const) : fail(400, "invalid_request");
}

function inboxSummary({ state }: Ctx<"/api/admin/v1/supplier-inbox/summary", "get">) {
  const total = inboxOf(state).length;
  const none = {
    low_balance: 0,
    ratio_changed: 0,
    cost_mismatch: 0,
    group_added: 0,
    unlisted_model_called: 0,
    key_invalid: 0,
  };
  return ok({ total, kinds: { ...none, unknown_model: total } });
}

function ignoreItem({ params, state }: Ctx<"/api/admin/v1/supplier-inbox/{item_id}/ignore", "post">) {
  const item = inboxOf(state).find((one) => one.id === params.item_id);
  if (!item) {
    return fail(404, "not_found");
  }
  ignored.set(state, (ignored.get(state) ?? new Set()).add(item.id));
  return ok({ ...item, ignored_at: "2026-11-26T08:00:00Z" });
}

export const supplyHandlers: Handler<AdminState>[] = [
  route("get", `${base}/supplier-inbox`, reads("suppliers.view"), listInbox),
  route("get", `${base}/supplier-inbox/summary`, reads("suppliers.view"), inboxSummary),
  route("post", `${base}/supplier-inbox/{item_id}/ignore`, changes("suppliers.manage"), ignoreItem),
  route("get", `${base}/supplier-types`, reads("suppliers.view"), listSupplierTypes),
  route("post", `${base}/supplier-types/{kind}/suppliers`, changes("suppliers.manage"), createTypedSupplier),
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
  route(
    "put",
    `${base}/suppliers/{supplier_id}/credentials/{credential_id}/limits`,
    changes("suppliers.manage"),
    setCredentialLimits,
  ),
  route(
    "put",
    `${base}/suppliers/{supplier_id}/endpoints/{endpoint_id}/enabled`,
    changes("suppliers.manage"),
    setEndpointEnabled,
  ),
  route("post", `${base}/suppliers/endpoint-tests`, changes("suppliers.manage"), testEndpoint),
  ...basePriceHandlers,
  route("get", `${base}/pending-mappings`, reads("suppliers.view"), listPending),
  route("post", `${base}/suppliers/{supplier_id}/model-mappings`, changes("suppliers.manage"), mapModel),
];
