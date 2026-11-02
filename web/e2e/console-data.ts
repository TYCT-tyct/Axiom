import type { components } from "../packages/api/src/console.gen.ts";
import type { AuthState } from "./mock.ts";
import { authState } from "./mock-auth.ts";

// The data the mocked console API starts from, written as the contract's own types, so a field the contract changes
// stops this file compiling. Three sets: a usual account, an empty one, and the worst realistic one (the longest values
// the contract allows, the biggest amounts, the most items), which the screenshots of the acceptance gate use.

export type Schemas = components["schemas"];

export type MockState = AuthState & {
  site: Schemas["Site"];
  balance: Schemas["Balance"];
  keys: Schemas["Key"][];
  passkeys: Schemas["Passkey"][];
  calls: Schemas["Call"][];
  models: Schemas["PublicModel"][];
  statement: Schemas["StatementRow"][];
  orders: Schemas["Order"][];
  methods: Schemas["PaymentMethod"][];
  notifications: Schemas["NotificationPreferences"];
};

const allPermissions = ["api_keys.view", "api_keys.manage", "balance.view", "balance.top_up", "calls.view"];
export const goodInvite = "INVITE-OK";

const usd = (units: string): Schemas["Money"] => ({ amount: `${units}000000000`, currency: "USD" });
const nano = (amount: string): Schemas["Money"] => ({ amount, currency: "USD" });

const keyId = (n: number) => `key_${String(n).padStart(26, "0")}` as Schemas["KeyId"];
const callId = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const workspace = `ws_${"0".repeat(26)}` as Schemas["WorkspaceId"];

export function aKey(n: number, over: Partial<Schemas["Key"]> = {}): Schemas["Key"] {
  return {
    id: keyId(n),
    name: `Key ${n}`,
    prefix: "sk-ax-v1-",
    last4: String(1000 + n),
    workspace_id: workspace,
    created_at: new Date(Date.UTC(2026, 9, 1, 8, n)).toISOString(),
    disabled: false,
    ...over,
  };
}

const models = ["anthropic/claude-sonnet-4", "openai/gpt-5", "google/gemini-2.5-pro", "deepseek/deepseek-v3"];
const apps = ["Claude Code", "Codex", "Cursor", ""];

function aCall(n: number, over: Partial<Schemas["Call"]> = {}): Schemas["Call"] {
  const failed = n % 7 === 0;
  return {
    id: callId(n) as Schemas["CallId"],
    at: new Date(Date.UTC(2026, 9, 14, 8, 30) - n * 60_000).toISOString(),
    workspace_id: workspace,
    key_id: keyId(1 + (n % 3)),
    ip: "203.0.113.7",
    client_app: apps[n % apps.length] ?? "",
    session_id: n % 2 === 0 ? `session-${n}` : "",
    subagent_id: "",
    request_class: "chat",
    model: models[n % models.length] ?? "",
    usage: failed ? {} : { input: 1200 + n, output: 300 + n, cache_read: 4000 },
    service_tier: "standard",
    price: failed || n % 11 === 0 ? usd("0") : nano(String(31_000 + n * 1_000)),
    free: n % 11 === 0 && !failed,
    latency_ms: 800 + n,
    first_token_ms: failed ? 0 : 240,
    finish_reason: failed ? "" : "stop",
    error_kind: failed ? "upstream_timeout" : "",
    ...over,
  };
}

function aModel(n: number, over: Partial<Schemas["PublicModel"]> = {}): Schemas["PublicModel"] {
  const abilities: NonNullable<Schemas["PublicModel"]["abilities"]> =
    n % 2 === 0 ? ["tools", "reasoning"] : ["tools", "images"];
  const pricing: Schemas["ModelPricing"] = {
    currency: "USD",
    basis: ["input", "output"],
    rates: [
      { service_tier: "standard", item: "input", unit: "token", amount: `0.00000${1 + (n % 9)}` },
      { service_tier: "standard", above: 128000, item: "input", unit: "token", amount: "0.00001" },
      { service_tier: "standard", item: "output", unit: "token", amount: `0.00000${2 + (n % 9)}` },
      { service_tier: "priority", above: 200000, item: "output", unit: "token", amount: "0.00002" },
    ],
    search_engines: [{ name: "exa", price_per_search: "0", price_per_extra_result: "0.001", default: true }],
  };
  // Two groups serve every model under the stable codenames a provider filter names. The second is dearer, unverified,
  // does not stream and has no recorded quantization or band price, so the per-endpoint schedule and attributes differ.
  const endpoints: Schemas["PublicEndpoint"][] = [
    {
      name: `ga2b3c4d${"234567"[n % 6]}`,
      context_length: 200000,
      max_output: 64000,
      streams: true,
      abilities,
      quantization: "fp8",
      verified: true,
      pricing,
    },
    {
      name: `ga2b3c4d${"234567"[(n + 10) % 6]}`,
      context_length: 100000,
      max_output: 32000,
      streams: false,
      abilities,
      verified: false,
      pricing: {
        ...pricing,
        rates: [
          { service_tier: "standard", item: "input", unit: "token", amount: `0.00000${4 + (n % 9)}` },
          { service_tier: "standard", item: "output", unit: "token", amount: `0.00000${5 + (n % 9)}` },
        ],
      },
    },
  ];
  return {
    id: models[n % models.length] ?? "",
    name: (models[n % models.length] ?? "").split("/")[1] ?? "",
    author: (models[n % models.length] ?? "").split("/")[0] ?? "",
    context_length: 200000,
    max_output: 64000,
    streams: true,
    abilities,
    pricing,
    endpoints,
    ...over,
  };
}

const range = (count: number) => Array.from({ length: count }, (_, i) => i + 1);

// A movement of the account's credits, newest first by n: a charge names one of the account's calls, and a gift, a
// top-up or an expiry names the batch it came from.
const kinds = ["spend", "spend", "top_up", "reversal", "gift", "expiry", "refund", "freeze", "unfreeze"] as const;

// What each kind does, in billionths: to the balance, to the credits frozen for refunds, and the frozen credits after.
const effects: Record<(typeof kinds)[number], [amount: string, frozenChange: string, frozenAfter: string]> = {
  spend: ["-1500000", "0", "0"],
  top_up: ["500000000", "0", "0"],
  reversal: ["500000000", "0", "0"],
  gift: ["500000000", "0", "0"],
  expiry: ["-1500000", "0", "0"],
  refund: ["-1500000", "-1500000", "0"],
  freeze: ["0", "1500000", "1500000"],
  unfreeze: ["0", "-1500000", "0"],
};

function aMovement(n: number, over: Partial<Schemas["StatementRow"]> = {}): Schemas["StatementRow"] {
  const kind = kinds[n % kinds.length] ?? "spend";
  const [amount, frozenChange, frozenAfter] = effects[kind];
  return {
    id: callId(1000 + n),
    at: new Date(Date.UTC(2026, 9, 14, 8, 30) - n * 60_000).toISOString(),
    kind,
    amount: nano(amount),
    balance_after: nano(String(3_000_000_000 - n * 100_000)),
    frozen_change: nano(frozenChange),
    frozen_after: nano(frozenAfter),
    reference: kind === "spend" || kind === "reversal" ? callId(1 + (n % 3)) : callId(2000 + n),
    ...over,
  };
}

const orderId = (n: number) => `ord_${String(n).padStart(26, "0")}` as Schemas["OrderId"];
const refundId = (n: number) => `rfd_${String(n).padStart(26, "0")}` as Schemas["RefundId"];
const cny = (fen: string): Schemas["PayAmount"] => ({ amount: fen, currency: "CNY" });
const states = [
  "pending",
  "paid",
  "partially_refunded",
  "refunded",
  "review",
  "expired",
  "cancelled",
  "failed",
] as const;

// A top-up, newest first by n, cycling through the states a person sees; the paid ones carry their refunds.
function anOrder(n: number, over: Partial<Schemas["Order"]> = {}): Schemas["Order"] {
  const state = states[n % states.length] ?? "paid";
  const at = Date.UTC(2026, 9, 14, 8, 30) - n * 3_600_000;
  const refund = (m: number, refundState: Schemas["RefundState"]): Schemas["OrderRefund"] => ({
    id: refundId(n * 10 + m),
    state: refundState,
    credits: usd("5"),
    amount: cny("3650"),
    created_at: new Date(at + m * 60_000).toISOString(),
  });
  const refunds: Record<string, Schemas["OrderRefund"][]> = {
    paid: [],
    partially_refunded: [refund(1, "succeeded"), refund(2, "pending_review")],
    refunded: [refund(1, "succeeded")],
  };
  return {
    id: orderId(n),
    state,
    method: n % 2 === 0 ? "alipay" : "wxpay",
    credits: usd("10"),
    total: cny("7373"),
    fee: cny("73"),
    created_at: new Date(at).toISOString(),
    expires_at: new Date(at + 15 * 60_000).toISOString(),
    checkout: state === "pending" ? { pay_url: "https://pay.example.com/checkout/1" } : undefined,
    refund_allowed: true,
    refunds: refunds[state] ?? [],
    ...over,
  };
}

const aBatch = (n: number, over: Partial<Schemas["CreditBatch"]> = {}): Schemas["CreditBatch"] => ({
  id: `00000000-0000-4000-9000-${String(n).padStart(12, "0")}`,
  source: n % 2 === 0 ? "gift" : "paid",
  amount: usd("10"),
  remaining: usd(String(10 - n)),
  frozen: usd("0"),
  created_at: new Date(Date.UTC(2026, 9, 1 + n)).toISOString(),
  ...over,
});

export function usualState(): MockState {
  return {
    ...authState(allPermissions),
    site: {
      name: "Axiom",
      currency: "USD",
      key_prefix: "sk-ax-v1-",
      api_base_url: "https://api.example.com",
      registration: "invite",
    },
    balance: {
      balance: usd("12"),
      reserved: nano("250000000"),
      floor: usd("0"),
      available: nano("11750000000"),
      spent_this_month: nano("3141592653"),
      batches: [
        aBatch(1, { frozen: usd("2"), expires_at: "2026-10-18T00:00:00Z" }),
        aBatch(2, { expires_at: "2027-01-01T00:00:00Z" }),
        aBatch(3),
      ],
    },
    keys: [
      aKey(3, { name: "Laptop", spend_limit: usd("20"), limit_reset: "monthly", limit_remaining: usd("17") }),
      aKey(2, { name: "CI runner", disabled: true, models: ["openai/gpt-5"], ip_allow: ["203.0.113.0/24"] }),
      aKey(1, { name: "Old key", expires_at: "2026-09-01T00:00:00Z" }),
    ],
    passkeys: [],
    calls: range(120).map((n) => aCall(n)),
    models: range(4).map((n) => aModel(n)),
    statement: range(120).map((n) => aMovement(n)),
    orders: range(12).map((n) => anOrder(n)),
    methods: [
      { method: "alipay", total: cny("7373"), fee: cny("73") },
      { method: "wxpay", total: cny("7380"), fee: cny("80") },
    ],
    notifications: {
      low_balance: true,
      top_up: true,
      factor_changed: true,
      spend_anomaly: true,
      threshold: "5000000000",
      webhook_url: "",
      language: "zh",
    },
  };
}

export function emptyState(): MockState {
  const state = { ...usualState(), keys: [], calls: [], models: [], statement: [], orders: [], methods: [] };
  state.balance = { ...state.balance, batches: [] };
  return state;
}

const long = (text: string, length: number) => text.repeat(Math.ceil(length / text.length)).slice(0, length);

// The longest values the contract allows, mixed over the rows: a 100-character key name, 200-character model names, a
// 254-character email, a 200-character API address, the biggest amounts, a debt, and a lot of rows.
export function worstState(): MockState {
  const state = usualState();
  state.user.email = `${long("bartholomew.fitzgerald-", 200)}@northwind-industries-holdings.example.com`;
  const host = [long("a", 63), long("b", 63), long("c", 63)].join(".");
  state.site.api_base_url = `https://${host}.example.com`;
  state.balance = {
    balance: nano("-1500000000"),
    reserved: nano("9223372036854775807"),
    floor: nano("-5000000000"),
    available: nano("-9223372036854775807"),
    spent_this_month: nano("9223372036854775807"),
  };
  state.keys = [
    aKey(1, {
      name: long("Production batch pipeline ", 100),
      spend_limit: nano("9223372036854775807"),
      limit_reset: "weekly",
      limit_remaining: nano("1"),
      models: range(256).map((n) => `vendor/model-${n}`),
      ip_allow: range(64).map((n) => `2001:db8:${n.toString(16)}::/48`),
    }),
    aKey(2, { name: "J" }),
    aKey(3, { name: "ケイ \u{1F511} 本番用", disabled: true }),
    ...range(40).map((n) => aKey(n + 3)),
  ];
  state.models = range(60).map((n) =>
    aModel(n, {
      id: `vendor-${n}/${"m".repeat(120)}-${n}`,
      name: "m".repeat(160),
      author: "a".repeat(120),
      abilities: [
        "tools",
        "images",
        "documents",
        "reasoning",
        "prompt_cache",
        "structured_output",
        "web_search",
        "code_execution",
      ],
    }),
  );
  state.calls = range(300).map((n) =>
    aCall(
      n,
      n === 1
        ? {
            model: long("vendor/a-very-long-model-name-", 200),
            client_app: long("Some Client ", 200),
            price: nano("9223372036854775807"),
          }
        : {},
    ),
  );
  state.statement = range(300).map((n) =>
    aMovement(n, n === 1 ? { amount: nano("-9223372036854775807"), balance_after: nano("-9223372036854775807") } : {}),
  );
  worstMoney(state, host);
  return state;
}

// The worst top-up data: int64 credits and payments, a 64-character method, six refunds on an order and many methods.
function worstMoney(state: MockState, host: string) {
  const huge = nano("9223372036854775807");
  const most = cny("9223372036854775807");
  state.balance.batches = range(40).map((n) =>
    aBatch(n, n === 1 ? { amount: huge, remaining: huge, frozen: huge, expires_at: "2026-10-01T00:00:00Z" } : {}),
  );
  const method = long("unionpay-international-", 64);
  state.orders = range(60).map((n) =>
    anOrder(
      n,
      n <= 3
        ? {
            method,
            credits: huge,
            total: most,
            fee: most,
            refunds: range(6).map((m) => ({
              id: refundId(n * 100 + m),
              state: (["pending_review", "processing", "succeeded", "declined"] as const)[m % 4] ?? "succeeded",
              credits: huge,
              amount: most,
              created_at: "2026-10-14T08:30:00Z",
            })),
          }
        : {},
    ),
  );
  state.methods = [
    { method, total: most, fee: most },
    ...range(8).map((n) => ({ method: `method-${n}`, total: cny(String(7000 + n)), fee: cny(String(n)) })),
  ];
  state.notifications = {
    ...state.notifications,
    threshold: "9223372036854775807",
    webhook_url: `https://${host}.example.com/hooks/axiom`,
  };
}
