import type { AdminState } from "./admin-data.ts";
import { long, type Schemas } from "./admin-fixtures.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { fail, type Handler, type MockData, ok, paged } from "./mock.ts";

// Mocked operations credentials, money cap policies, agent rate limits, price envelopes and emergency stop.
type Credential = Schemas["OperationsCredential"];
type CapPolicy = Schemas["OperationsCapPolicy"];
type AgentLimits = Schemas["OperationsAgentLimits"];
type Envelope = Schemas["OperationsPriceEnvelopeSetting"];

type OpsControlStore = {
  credentials: Credential[];
  caps: CapPolicy[];
  agentLimits: AgentLimits;
  envelopes: Envelope[];
  nextId: number;
};

const stores = new WeakMap<AdminState, OpsControlStore>();
const nanos = (units: string) => `${units}000000000`;
const credId = (n: number) => `opcred_${String(n).padStart(26, "0")}`;

function usualCredentials(state: AdminState, now: number): Credential[] {
  return [
    {
      id: credId(1),
      name: "hermes-prod",
      permissions: ["operations.money", "operations.read", "operations.write", "pricing.manage"],
      sites: [state.siteId],
      platform: false,
      proposer_vendors: ["deepseek", "anthropic"],
      created_at: new Date(now - 10 * 86_400_000).toISOString(),
      expires_at: new Date(now + 20 * 86_400_000).toISOString(),
      last_used_at: new Date(now - 5 * 60_000).toISOString(),
      token_last4: "a1b2",
    },
    {
      id: credId(2),
      name: "watch-readonly",
      permissions: ["calls.search", "operations.read"],
      platform: true,
      proposer_vendors: ["openai"],
      created_at: new Date(now - 20 * 86_400_000).toISOString(),
      expires_at: new Date(now + 40 * 86_400_000).toISOString(),
      previous_token_until: new Date(now + 6 * 3_600_000).toISOString(),
      token_last4: "c3d4",
    },
    {
      id: credId(3),
      name: "legacy-bot",
      permissions: ["operations.read"],
      platform: false,
      proposer_vendors: ["deepseek"],
      created_at: "2026-08-01T00:00:00Z",
      expires_at: "2026-12-01T00:00:00Z",
      revoked_at: "2026-09-15T10:00:00Z",
      token_last4: "e5f6",
    },
  ];
}

function usualCaps(state: AdminState, now: number): CapPolicy[] {
  return [
    {
      scope: "platform",
      category: "money.top_up",
      currency: "USD",
      limits: {
        single_max: nanos("50"),
        daily_max: nanos("200"),
        weekly_max: nanos("1000"),
        object_daily_max: nanos("100"),
        daily_count_max: 10,
        hard_single_max: nanos("500"),
        hard_daily_max: nanos("2000"),
        hard_weekly_max: nanos("8000"),
      },
      rev: 3,
      updated_at: "2026-10-20T09:00:00Z",
    },
    {
      scope: state.siteId,
      category: "money.refund",
      currency: "USD",
      limits: {
        single_max: nanos("25"),
        daily_max: nanos("100"),
        weekly_max: nanos("500"),
        object_daily_max: nanos("50"),
        daily_count_max: 5,
        hard_single_max: nanos("200"),
        hard_daily_max: nanos("1000"),
        hard_weekly_max: nanos("4000"),
      },
      rev: 2,
      updated_at: "2026-10-21T11:30:00Z",
      paused_at: new Date(now - 2 * 3_600_000).toISOString(),
    },
  ];
}

function usualAgentLimits(): AgentLimits {
  return {
    requests_per_minute: 300,
    requests_per_minute_max: 600,
    money_writes_per_minute: 10,
    money_writes_per_minute_max: 30,
    money_writes_per_hour: 120,
    money_writes_per_hour_max: 300,
    alerts_per_hour: 30,
    alerts_per_hour_max: 60,
    rev: 2,
    updated_at: "2026-10-18T14:00:00Z",
  };
}

function usualStore(state: AdminState): OpsControlStore {
  const now = Date.now();
  return {
    credentials: usualCredentials(state, now),
    caps: usualCaps(state, now),
    agentLimits: usualAgentLimits(),
    envelopes: [
      {
        site_id: state.siteId,
        limits: { min_margin_bps: 1500, model_daily_change_bps: 1000, daily_changes: 20, daily_models: 5 },
        rev: 2,
        updated_at: "2026-10-19T08:00:00Z",
      },
    ],
    nextId: 10,
  };
}

function worstStore(state: AdminState): OpsControlStore {
  const store = usualStore(state);
  if (store.credentials[0]) {
    store.credentials[0].name = long("hermes-production-operations-agent-", 96);
    store.credentials[0].permissions = [
      "audit.view",
      "calls.inspect",
      "calls.search",
      "catalog.manage",
      "credits.grant",
      "operations.money",
      "operations.read",
      "operations.write",
      "pricing.manage",
      "sites.manage",
      "suppliers.manage",
    ];
  }
  if (store.caps[0]) {
    store.caps[0].limits.hard_weekly_max = "999999999000000000";
  }
  return store;
}

export function startOpsControl(state: AdminState, data: MockData) {
  const store =
    data === "empty"
      ? {
          credentials: [],
          caps: [],
          agentLimits: { ...usualAgentLimits(), rev: 0, updated_at: undefined },
          envelopes: [],
          nextId: 10,
        }
      : data === "worst"
        ? worstStore(state)
        : usualStore(state);
  stores.set(state, store);
}

export function opsControlOf(state: AdminState): OpsControlStore {
  let current = stores.get(state);
  if (!current) {
    current = usualStore(state);
    stores.set(state, current);
  }
  return current;
}

function listCredentials({ query, state }: Ctx<"/api/admin/v1/operations/credentials", "get">) {
  const page = paged(opsControlOf(state).credentials, query);
  return page ? ok({ data: page.data, next_cursor: page.next_cursor ?? undefined }) : fail(400, "invalid_request");
}

function createCredential({ body, state }: Ctx<"/api/admin/v1/operations/credentials", "post">) {
  const store = opsControlOf(state);
  const forbidden = new Set(["operations.manage", "operations.approve", "platform_grants.manage"]);
  if (body.permissions.some((perm) => forbidden.has(perm))) {
    return fail(400, "invalid_request", { "/permissions": "permission cannot be granted" });
  }
  store.nextId += 1;
  const id = credId(store.nextId);
  const last4 = String(store.nextId).padStart(4, "0");
  const permissions = [...new Set(["operations.read", ...body.permissions])].sort();
  store.credentials.unshift({
    id,
    name: body.name,
    permissions,
    ...(body.sites ? { sites: body.sites } : {}),
    platform: body.platform,
    proposer_vendors: body.proposer_vendors,
    created_at: new Date().toISOString(),
    expires_at: body.expires_at,
    token_last4: last4,
  });
  return { status: 201 as const, json: { credential_id: id, token: `op-ax-v1-mock-${last4}` } };
}

function rotateCredential({
  body,
  params,
  state,
}: Ctx<"/api/admin/v1/operations/credentials/{credential_id}/rotate", "post">) {
  const store = opsControlOf(state);
  const cred = store.credentials.find((item) => item.id === params.credential_id);
  if (!cred) {
    return fail(404, "not_found");
  }
  if (cred.revoked_at) {
    return fail(409, "conflict");
  }
  store.nextId += 1;
  const last4 = `r${String(store.nextId).padStart(3, "0")}`;
  const overlap = body.overlap_hours ?? 24;
  cred.token_last4 = last4;
  cred.previous_token_until = overlap > 0 ? new Date(Date.now() + overlap * 3_600_000).toISOString() : undefined;
  return ok({ credential_id: cred.id, token: `op-ax-v1-rot-${last4}` });
}

function revokeCredential({
  params,
  state,
}: Ctx<"/api/admin/v1/operations/credentials/{credential_id}/revoke", "post">) {
  const cred = opsControlOf(state).credentials.find((item) => item.id === params.credential_id);
  if (!cred) {
    return fail(404, "not_found");
  }
  cred.revoked_at = new Date().toISOString();
  return ok({ credential_id: cred.id });
}

function putCapPolicy({ body, params, state }: Ctx<"/api/admin/v1/operations/cap-policies/{scope}/{category}", "put">) {
  const store = opsControlOf(state);
  const scope = params.scope ?? "platform";
  const category = (params.category ?? "money.top_up") as Schemas["OperationsCapCategory"];
  const existing = store.caps.find((item) => item.scope === scope && item.category === category);
  if ((existing?.rev ?? 0) !== body.rev) {
    return fail(409, "stale_revision");
  }
  const now = new Date().toISOString();
  if (existing) {
    existing.limits = body.limits;
    existing.rev += 1;
    existing.updated_at = now;
    return ok(existing);
  }
  const created: CapPolicy = { scope, category, currency: body.currency, limits: body.limits, rev: 1, updated_at: now };
  store.caps.push(created);
  return ok(created);
}

function repairCapPolicy({
  params,
  state,
}: Ctx<"/api/admin/v1/operations/cap-policies/{scope}/{category}/repair", "post">) {
  const policy = opsControlOf(state).caps.find(
    (item) => item.scope === params.scope && item.category === params.category,
  );
  if (!policy) {
    return fail(404, "not_found");
  }
  policy.paused_at = undefined;
  policy.updated_at = new Date().toISOString();
  return ok(policy);
}

function putAgentLimits({ body, state }: Ctx<"/api/admin/v1/operations/agent-limits", "put">) {
  const store = opsControlOf(state);
  if ((store.agentLimits.rev ?? 0) !== body.rev) {
    return fail(409, "stale_revision");
  }
  store.agentLimits = {
    ...store.agentLimits,
    ...(body.requests_per_minute !== undefined ? { requests_per_minute: body.requests_per_minute } : {}),
    ...(body.money_writes_per_minute !== undefined ? { money_writes_per_minute: body.money_writes_per_minute } : {}),
    ...(body.money_writes_per_hour !== undefined ? { money_writes_per_hour: body.money_writes_per_hour } : {}),
    alerts_per_hour: body.alerts_per_hour,
    rev: (store.agentLimits.rev ?? 0) + 1,
    updated_at: new Date().toISOString(),
  };
  return ok(store.agentLimits);
}

function putPriceEnvelope({ body, params, state }: Ctx<"/api/admin/v1/operations/price-envelopes/{site_id}", "put">) {
  const store = opsControlOf(state);
  const siteId = params.site_id ?? state.siteId;
  const existing = store.envelopes.find((item) => item.site_id === siteId);
  if ((existing?.rev ?? 0) !== body.rev) {
    return fail(409, "stale_revision");
  }
  const now = new Date().toISOString();
  if (existing) {
    existing.limits = body.limits;
    existing.rev += 1;
    existing.updated_at = now;
    return ok(existing);
  }
  const created: Envelope = { site_id: siteId, limits: body.limits, rev: 1, updated_at: now };
  store.envelopes.push(created);
  return ok(created);
}

function stopAgent({ state }: Ctx<"/api/admin/v1/operations/agent/stop", "post">) {
  const now = new Date().toISOString();
  let revoked = 0;
  for (const cred of opsControlOf(state).credentials) {
    if (!cred.revoked_at && Date.parse(cred.expires_at) > Date.now()) {
      cred.revoked_at = now;
      revoked += 1;
    }
  }
  return ok({ revoked, canceled: 4 });
}

export const opsControlHandlers: Handler<AdminState>[] = [
  route("get", `${base}/operations/credentials`, reads("operations.manage"), listCredentials),
  route("post", `${base}/operations/credentials`, changes("operations.manage"), createCredential),
  route(
    "post",
    `${base}/operations/credentials/{credential_id}/rotate`,
    changes("operations.manage"),
    rotateCredential,
  ),
  route(
    "post",
    `${base}/operations/credentials/{credential_id}/revoke`,
    changes("operations.manage"),
    revokeCredential,
  ),
  route("get", `${base}/operations/cap-policies`, reads("operations.manage"), ({ state }) =>
    ok({ data: opsControlOf(state).caps }),
  ),
  route("put", `${base}/operations/cap-policies/{scope}/{category}`, changes("operations.manage"), putCapPolicy),
  route(
    "post",
    `${base}/operations/cap-policies/{scope}/{category}/repair`,
    changes("operations.manage"),
    repairCapPolicy,
  ),
  route("get", `${base}/operations/agent-limits`, reads("operations.manage"), ({ state }) =>
    ok(opsControlOf(state).agentLimits),
  ),
  route("put", `${base}/operations/agent-limits`, changes("operations.manage"), putAgentLimits),
  route("get", `${base}/operations/price-envelopes`, reads("operations.manage"), ({ state }) =>
    ok({ data: opsControlOf(state).envelopes }),
  ),
  route("put", `${base}/operations/price-envelopes/{site_id}`, changes("operations.manage"), putPriceEnvelope),
  route("post", `${base}/operations/agent/stop`, changes("operations.manage"), stopAgent),
];
