import type { AdminState } from "./admin-data.ts";
import { long, type Schemas, uuid } from "./admin-fixtures.ts";
import { base, type Ctx, changes, reads, route } from "./admin-route.ts";
import { fail, type Handler, type MockData, ok } from "./mock.ts";
import { userId } from "./mock-auth.ts";

// Mocked subscription plans, account subscriptions and two-person refund proposals for admin e2e.
type Plan = Schemas["SubscriptionPlan"];
type Subscription = Schemas["Subscription"];
type RefundProposal = Schemas["SubscriptionRefundProposal"] & { account_id: string };

type SubscriptionStore = {
  plans: Plan[];
  subscriptions: Map<string, Subscription[]>;
  proposals: Map<string, RefundProposal>;
  nextId: number;
};

const stores = new WeakMap<AdminState, SubscriptionStore>();
const nanos = (units: string) => `${units}000000000`;
const acct = (n: number) => `acct_${String(n).padStart(26, "0")}`;

export const pendingRefundOtherId = uuid(24, 1);
export const pendingRefundSelfId = uuid(24, 2);
export const paidGrantId = uuid(23, 1);

function usualPlans(): Plan[] {
  return [
    {
      id: uuid(21, 1),
      name: "Pro Monthly",
      price: { amount: nanos("29"), currency: "USD" },
      quota: nanos("5"),
      window_seconds: 86_400,
      term_seconds: 2_592_000,
      active: true,
      rev: 2,
      created_at: "2026-09-01T00:00:00Z",
      updated_at: "2026-10-15T12:00:00Z",
    },
    {
      id: uuid(21, 2),
      name: "Team Monthly",
      price: { amount: nanos("99"), currency: "USD" },
      quota: nanos("20"),
      window_seconds: 86_400,
      term_seconds: 2_592_000,
      active: true,
      rev: 1,
      created_at: "2026-09-10T00:00:00Z",
      updated_at: "2026-09-10T00:00:00Z",
    },
    {
      id: uuid(21, 3),
      name: "Legacy Starter",
      price: { amount: nanos("9"), currency: "USD" },
      quota: nanos("1"),
      window_seconds: 86_400,
      term_seconds: 604_800,
      active: false,
      rev: 3,
      created_at: "2026-08-01T00:00:00Z",
      updated_at: "2026-10-01T08:00:00Z",
    },
  ];
}

function usualSubscriptions(now: number): Map<string, Subscription[]> {
  const accountId = acct(1);
  return new Map<string, Subscription[]>([
    [
      accountId,
      [
        {
          id: uuid(22, 1),
          account_id: accountId,
          plan_id: uuid(21, 1),
          plan_name: "Pro Monthly",
          state: "active",
          starts_at: new Date(now - 3 * 86_400_000).toISOString(),
          ends_at: new Date(now + 27 * 86_400_000).toISOString(),
          created_at: new Date(now - 3 * 86_400_000).toISOString(),
          current_window: {
            seq: 4,
            starts_at: new Date(now - 6 * 3_600_000).toISOString(),
            ends_at: new Date(now + 18 * 3_600_000).toISOString(),
            quota: nanos("5"),
            used: "1500000000",
            reserved: "250000000",
            remaining: "3250000000",
          },
        },
        {
          id: uuid(22, 2),
          account_id: accountId,
          plan_id: uuid(21, 3),
          plan_name: "Legacy Starter",
          state: "expired",
          starts_at: "2026-08-10T00:00:00Z",
          ends_at: "2026-08-17T00:00:00Z",
          closed_at: "2026-08-17T00:05:00Z",
          created_at: "2026-08-10T00:00:00Z",
          current_window: {
            seq: 7,
            starts_at: "2026-08-16T00:00:00Z",
            ends_at: "2026-08-17T00:00:00Z",
            quota: nanos("1"),
            used: "800000000",
            reserved: "0",
            remaining: "200000000",
          },
        },
      ],
    ],
  ]);
}

function usualProposals(state: AdminState, now: number): Map<string, RefundProposal> {
  const accountId = acct(1);
  return new Map<string, RefundProposal>([
    [
      pendingRefundOtherId,
      {
        id: pendingRefundOtherId,
        account_id: accountId,
        grant_id: paidGrantId,
        credits: nanos("10"),
        state: "pending",
        requested_by: uuid(99, 2),
        created_at: new Date(now - 30 * 60_000).toISOString(),
      },
    ],
    [
      pendingRefundSelfId,
      {
        id: pendingRefundSelfId,
        account_id: accountId,
        grant_id: paidGrantId,
        credits: nanos("5"),
        state: "pending",
        requested_by: userId(state.account),
        created_at: new Date(now - 10 * 60_000).toISOString(),
      },
    ],
  ]);
}

function usualStore(state: AdminState): SubscriptionStore {
  const now = Date.now();
  return {
    plans: usualPlans(),
    subscriptions: usualSubscriptions(now),
    proposals: usualProposals(state, now),
    nextId: 10,
  };
}

function worstStore(state: AdminState): SubscriptionStore {
  const store = usualStore(state);
  const first = store.plans[0];
  if (first) {
    first.name = long("Enterprise Unlimited High-Throughput Subscription Plan ", 180);
    first.price = { amount: "999999000000000", currency: "USD" };
    first.quota = "500000000000000";
  }
  const subs = store.subscriptions.get(acct(1));
  if (subs?.[0]) {
    subs[0].plan_name = long("Enterprise Unlimited High-Throughput Subscription Plan ", 180);
    subs[0].current_window.quota = "500000000000000";
    subs[0].current_window.used = "123456789000000";
    subs[0].current_window.remaining = "376543211000000";
  }
  return store;
}

export function startSubscriptions(state: AdminState, data: MockData) {
  const store =
    data === "empty"
      ? { plans: [], subscriptions: new Map(), proposals: new Map(), nextId: 10 }
      : data === "worst"
        ? worstStore(state)
        : usualStore(state);
  stores.set(state, store);
}

export function subscriptionsOf(state: AdminState): SubscriptionStore {
  let current = stores.get(state);
  if (!current) {
    current = usualStore(state);
    stores.set(state, current);
  }
  return current;
}

function listPlans({ query, state }: Ctx<"/api/admin/v1/subscription-plans", "get">) {
  const includeInactive = query.get("include_inactive") === "true";
  const data = subscriptionsOf(state).plans.filter((plan) => includeInactive || plan.active);
  return ok({ data });
}

function createPlan({ body, state }: Ctx<"/api/admin/v1/subscription-plans", "post">) {
  const store = subscriptionsOf(state);
  if (body.window_seconds < 3600 || body.term_seconds < body.window_seconds) {
    return fail(422, "unprocessable");
  }
  if (store.plans.some((plan) => plan.name === body.name)) {
    return fail(409, "conflict");
  }
  const now = new Date().toISOString();
  store.nextId += 1;
  const plan: Plan = {
    id: uuid(21, store.nextId),
    name: body.name,
    price: body.price,
    quota: body.quota,
    window_seconds: body.window_seconds,
    term_seconds: body.term_seconds,
    active: body.active ?? true,
    rev: 1,
    created_at: now,
    updated_at: now,
  };
  store.plans.unshift(plan);
  return { status: 201 as const, json: plan };
}

function updatePlan({ body, params, state }: Ctx<"/api/admin/v1/subscription-plans/{plan_id}", "put">) {
  const store = subscriptionsOf(state);
  const plan = store.plans.find((candidate) => candidate.id === params.plan_id);
  if (!plan) {
    return fail(404, "not_found");
  }
  if (plan.rev !== body.rev) {
    return fail(409, "stale_revision");
  }
  plan.name = body.name;
  plan.price = body.price;
  plan.quota = body.quota;
  plan.window_seconds = body.window_seconds;
  plan.term_seconds = body.term_seconds;
  plan.active = body.active ?? true;
  plan.rev += 1;
  plan.updated_at = new Date().toISOString();
  return ok(plan);
}

function listAccountSubscriptions({ params, state }: Ctx<"/api/admin/v1/accounts/{account_id}/subscriptions", "get">) {
  const accountId = params.account_id ?? "";
  if (!state.accounts.has(accountId)) {
    return fail(404, "not_found");
  }
  return ok({ data: subscriptionsOf(state).subscriptions.get(accountId) ?? [] });
}

function grantSubscription({
  body,
  params,
  state,
}: Ctx<"/api/admin/v1/accounts/{account_id}/subscription-grants", "post">) {
  const accountId = params.account_id ?? "";
  if (!state.accounts.has(accountId)) {
    return fail(404, "not_found");
  }
  const store = subscriptionsOf(state);
  const plan = store.plans.find((candidate) => candidate.id === body.plan_id);
  if (!plan) {
    return fail(404, "not_found");
  }
  if (!plan.active) {
    return fail(409, "conflict");
  }
  const list = store.subscriptions.get(accountId) ?? [];
  const existing = list.find((sub) => sub.state === "active");
  if (existing && existing.plan_id !== plan.id) {
    return fail(409, "conflict");
  }
  const now = Date.now();
  if (existing) {
    existing.ends_at = new Date(Date.parse(existing.ends_at) + plan.term_seconds * 1000).toISOString();
    return { status: 201 as const, json: existing };
  }
  store.nextId += 1;
  const created: Subscription = {
    id: uuid(22, store.nextId),
    account_id: accountId,
    plan_id: plan.id,
    plan_name: plan.name,
    state: "active",
    starts_at: new Date(now).toISOString(),
    ends_at: new Date(now + plan.term_seconds * 1000).toISOString(),
    created_at: new Date(now).toISOString(),
    current_window: {
      seq: 1,
      starts_at: new Date(now).toISOString(),
      ends_at: new Date(now + plan.window_seconds * 1000).toISOString(),
      quota: plan.quota,
      used: "0",
      reserved: "0",
      remaining: plan.quota,
    },
  };
  list.unshift(created);
  store.subscriptions.set(accountId, list);
  return { status: 201 as const, json: created };
}

function requestRefund({
  body,
  params,
  state,
}: Ctx<"/api/admin/v1/accounts/{account_id}/subscription-grants/{grant_id}/refunds", "post">) {
  const accountId = params.account_id ?? "";
  if (!state.accounts.has(accountId)) {
    return fail(404, "not_found");
  }
  if (BigInt(body.credits) <= 0n) {
    return fail(422, "unprocessable", { "/credits": "must be positive" });
  }
  const store = subscriptionsOf(state);
  store.nextId += 1;
  const proposal: RefundProposal = {
    id: uuid(24, store.nextId),
    account_id: accountId,
    grant_id: params.grant_id ?? "",
    credits: body.credits,
    state: "pending",
    requested_by: userId(state.account),
    created_at: new Date().toISOString(),
  };
  store.proposals.set(proposal.id, proposal);
  const { account_id: _, ...out } = proposal;
  return { status: 202 as const, json: out };
}

function approveRefund({
  params,
  state,
}: Ctx<"/api/admin/v1/accounts/{account_id}/subscription-refunds/{refund_id}/approve", "post">) {
  const accountId = params.account_id ?? "";
  const account = state.accounts.get(accountId);
  if (!account) {
    return fail(404, "not_found");
  }
  const store = subscriptionsOf(state);
  const proposal = store.proposals.get(params.refund_id ?? "");
  if (!proposal || proposal.account_id !== accountId) {
    return fail(404, "not_found");
  }
  if (proposal.state !== "pending" || proposal.requested_by === userId(state.account)) {
    return fail(409, "conflict");
  }
  proposal.state = "approved";
  proposal.approved_by = userId(state.account);
  proposal.finished_at = new Date().toISOString();
  return ok({
    id: proposal.id,
    grant_id: proposal.grant_id,
    credits: proposal.credits,
    amount: { amount: proposal.credits, currency: account.balance.balance.currency },
    windows: 3,
    unused: proposal.credits,
    revoked: false,
    created_at: proposal.finished_at,
  });
}

export const subscriptionHandlers: Handler<AdminState>[] = [
  route("get", `${base}/subscription-plans`, reads("pricing.view"), listPlans),
  route("post", `${base}/subscription-plans`, changes("pricing.manage"), createPlan),
  route("put", `${base}/subscription-plans/{plan_id}`, changes("pricing.manage"), updatePlan),
  route("get", `${base}/accounts/{account_id}/subscriptions`, reads("users.view"), listAccountSubscriptions),
  route("post", `${base}/accounts/{account_id}/subscription-grants`, changes("credits.grant"), grantSubscription),
  route(
    "post",
    `${base}/accounts/{account_id}/subscription-grants/{grant_id}/refunds`,
    changes("credits.grant"),
    requestRefund,
  ),
  route(
    "post",
    `${base}/accounts/{account_id}/subscription-refunds/{refund_id}/approve`,
    changes("credits.grant"),
    approveRefund,
  ),
];
