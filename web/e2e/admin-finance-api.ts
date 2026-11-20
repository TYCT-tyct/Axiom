import type { AdminState, Schemas } from "./admin-data.ts";
import { long, pad, uuid } from "./admin-fixtures.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { fail, type Handler, type MockData, ok } from "./mock.ts";

type Rate = Schemas["ExchangeRate"];
type Discrepancy = Schemas["PaymentDiscrepancy"];
type Grant = Schemas["PlatformGrant"];

const states = new WeakMap<AdminState, { rates: Rate[]; discrepancies: Discrepancy[]; grants: Map<string, Grant[]> }>();
const policies = new WeakMap<AdminState, Schemas["PricePolicy"]>();
const at = "2026-11-01T08:00:00Z";
const created = <T>(json: T) => ({ status: 201 as const, json });

export function pricePolicy(state: AdminState) {
  return (
    policies.get(state) ?? {
      rev: 0,
      floor_multiplier: "1",
      change_ratio: "0.1",
      max_increase_ratio: "0.5",
      byok_fee_bps: 500,
    }
  );
}

function updatePricePolicy({ body, state }: Ctx<"/api/admin/v1/pricing/policy", "put">) {
  const previous = pricePolicy(state);
  if (previous.rev !== body.rev) return fail(409, "stale_revision");
  const { reason: _reason, ...settings } = body;
  const policy = { ...settings, rev: body.rev + 1, byok_fee_bps: body.byok_fee_bps ?? previous.byok_fee_bps };
  policies.set(state, policy);
  return ok({ policy, data: [] });
}

function fixtureRates(data: MockData): Rate[] {
  if (data === "empty") return [];
  const first: Rate = {
    id: uuid(34, 1),
    base: "CNY",
    quote: "USD",
    numerator: "10",
    denominator: "71",
    version: 1,
    source: "manual",
    kind: "initial",
    reason: "Initial pair from finance",
    created_by: "operator",
    created_at: at,
  };
  return [
    { ...first, id: uuid(34, 2), denominator: "72", version: 2, kind: "adjustment", reason: "Weekly review" },
    first,
  ];
}

function fixtureDiscrepancy(state: AdminState, data: MockData): Discrepancy {
  const rawJSON = (JSON as typeof JSON & { rawJSON: (value: string) => unknown }).rawJSON;
  return {
    id: uuid(33, 1),
    site_id: state.siteId,
    account_id: `acct_${pad(1)}`,
    order_id: `ord_${pad(1)}`,
    merchant_id: `mch_${pad(1)}`,
    kind: data === "worst" ? long("daily_reconciliation_difference_", 128) : "payment_amount_mismatch",
    state: "open",
    created_at: at,
    detail: {
      trade_no: "processor-reference-219",
      expected: rawJSON("9007199254740993"),
      actual: "9007199254740992",
      unit: "USD cents",
    },
  };
}

export function startFinance(state: AdminState, data: MockData) {
  states.set(state, {
    rates: fixtureRates(data),
    discrepancies: data === "empty" ? [] : [fixtureDiscrepancy(state, data)],
    grants: new Map(),
  });
}

function finance(state: AdminState) {
  const current = states.get(state);
  if (!current) throw new Error("missing admin finance fixtures");
  return current;
}

function grantRows(state: AdminState, userId: string) {
  const { grants } = finance(state);
  const existing = grants.get(userId);
  if (existing) return existing;
  const rows = state.users.some((user) => user.id === userId)
    ? [
        { role: "support", revoked: false },
        { role: "viewer", revoked: true },
      ]
    : [];
  grants.set(userId, rows);
  return rows;
}

function setGrant({ params, body, state }: Ctx<"/api/admin/v1/users/{user_id}/platform-grants/{role}", "put">) {
  const row = grantRows(state, params.user_id ?? "").find((grant) => grant.role === params.role);
  if (!row) return fail(404, "not_found");
  row.revoked = body.revoked;
  return { status: 204 as const, json: undefined };
}

function getDiscrepancy({ params, state }: Ctx<"/api/admin/v1/payments/discrepancies/{discrepancy_id}", "get">) {
  const row = finance(state).discrepancies.find((item) => item.id === params.discrepancy_id);
  return row ? ok(row) : fail(404, "not_found");
}

function investigate({
  params,
  body,
  state,
}: Ctx<"/api/admin/v1/payments/discrepancies/{discrepancy_id}/investigate", "post">) {
  const row = finance(state).discrepancies.find((item) => item.id === params.discrepancy_id);
  if (!row) return fail(404, "not_found");
  if (row.state === "resolved") return fail(409, "conflict");
  Object.assign(row, { state: "resolved", investigated_at: at, investigated_by: "operator", note: body.reason });
  return created(row);
}

const hasPair = (row: Rate, a: string, b: string) => row.base === a && row.quote === b;
const pairs = (rates: Rate[]) => [
  ...new Map(rates.toReversed().map((row) => [`${row.base}/${row.quote}`, row])).values(),
];

function setExchange({ body, state }: Ctx<"/api/admin/v1/pricing/exchange-rates", "post">) {
  const rates = finance(state).rates;
  const reverse = body.base > body.quote;
  const [base, quote] = [body.base, body.quote].sort();
  if (!base || !quote || base === quote) return fail(422, "unprocessable");
  const previous = rates.find((row) => hasPair(row, base, quote));
  if ((previous?.version ?? 0) !== body.expected_version) return fail(409, "stale_revision");
  const row: Rate = {
    id: uuid(34, rates.length + 1),
    base,
    quote,
    numerator: reverse ? body.denominator : body.numerator,
    denominator: reverse ? body.numerator : body.denominator,
    version: body.expected_version + 1,
    source: "manual",
    kind: body.kind,
    reason: body.reason,
    created_by: "operator",
    created_at: at,
  };
  rates.unshift(row);
  return created(row);
}

export const financeHandlers: Handler<AdminState>[] = [
  route("get", `${base}/payments/discrepancies`, reads("sites.manage"), ({ query, state }) => {
    const filter = query.get("state");
    const before = query.get("before");
    return ok({
      data: finance(state)
        .discrepancies.filter((row) => (!filter || row.state === filter) && (!before || (row.id ?? "") < before))
        .slice(0, 100),
    });
  }),
  route("get", `${base}/payments/discrepancies/{discrepancy_id}`, reads("sites.manage"), getDiscrepancy),
  route("post", `${base}/payments/discrepancies/{discrepancy_id}/investigate`, changes("sites.manage"), investigate),
  route("get", `${base}/pricing/exchange-rates`, reads("pricing.view"), ({ state }) =>
    ok({ data: pairs(finance(state).rates) }),
  ),
  route("post", `${base}/pricing/exchange-rates`, changes("pricing.manage"), setExchange),
  route("get", `${base}/pricing/exchange-rates/{base}/{quote}`, reads("pricing.view"), ({ params, query, state }) => {
    const [a, b] = [params.base ?? "", params.quote ?? ""].sort();
    const before = Number(query.get("before_version") ?? Infinity);
    return ok({
      data: finance(state)
        .rates.filter((row) => hasPair(row, a ?? "", b ?? "") && row.version < before)
        .slice(0, 100),
    });
  }),
  route("put", `${base}/pricing/policy`, changes("pricing.manage"), updatePricePolicy),
  route("get", `${base}/users/{user_id}/platform-grants`, reads("platform_grants.manage"), ({ params, state }) =>
    ok({ data: grantRows(state, params.user_id ?? "") }),
  ),
  route("put", `${base}/users/{user_id}/platform-grants/{role}`, changes("platform_grants.manage"), setGrant),
];
