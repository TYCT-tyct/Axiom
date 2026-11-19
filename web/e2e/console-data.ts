import type { components } from "../packages/api/src/console.gen.ts";
import {
  type AccountMockFields,
  emptyAccountFields,
  usualAccountFields,
  worstAccountFields,
} from "./console-account-mock.ts";
import { type BillMockState, emptyBillState, usualBillState, worstBillDaily } from "./console-bill-data.ts";
import { buildMockCall } from "./console-calls-data.ts";
import { mailedLinks } from "./mail-links.ts";
import type { AuthState } from "./mock.ts";
import { authState } from "./mock-auth.ts";

// The data the mocked console API starts from, written as the contract's own types, so a field the contract changes
// stops this file compiling. Three sets: a usual account, an empty one, and the worst realistic one (the longest values
// the contract allows, the biggest amounts, the most items), which the screenshots of the acceptance gate use.

export type Schemas = components["schemas"];

export type MockState = AuthState &
  AccountMockFields &
  BillMockState & {
    site: Schemas["Site"];
    legal: Schemas["LegalDocument"][];
    legalAccepted: boolean;
    balance: Schemas["Balance"];
    keys: Schemas["Key"][];
    passkeys: Schemas["Passkey"][];
    calls: Schemas["Call"][];
    models: Schemas["PublicModel"][];
    statement: Schemas["StatementRow"][];
    orders: Schemas["Order"][];
    presets: Schemas["RoutingPreset"][];
    routingDefault: Schemas["AccountRoutingDefault"];
    usageBackfillComplete?: boolean;
    // the reset links the server has mailed and not yet spent, by token: the address each is for
    resetLinks: Map<string, string>;
    // the verification links an open registration has mailed and not yet spent, by token: the address each is for
    verifyLinks: Map<string, string>;
  };

const allPermissions = ["api_keys.view", "api_keys.manage", "balance.view", "balance.top_up", "calls.view"];
// The legal documents a site publishes, in both languages, as the consent and registration pages show them.
export const demoDocuments = (): Schemas["LegalDocument"][] => [
  {
    kind: "terms",
    locale: "zh",
    version: 3,
    body: "【示例文本（demo），不构成法律意见，正式上线前须由负责人和律师审定替换】\n\n# Axiom 服务条款（示例）\n\n## 服务说明\n\n这是服务条款的演示文本。",
    published_at: "2026-11-04T00:00:00Z",
  },
  {
    kind: "privacy",
    locale: "zh",
    version: 2,
    body: "【示例文本（demo），不构成法律意见，正式上线前须由负责人和律师审定替换】\n\n# Axiom 隐私政策（示例）\n\n## 引言与适用范围\n\n这是隐私政策的演示文本。",
    published_at: "2026-11-04T00:00:00Z",
  },
  {
    kind: "terms",
    locale: "en",
    version: 1,
    body: "Demo text, not legal advice; replace before launch.\n\n# Axiom Terms of Service (Demo)\n\n## The Service\n\nThis is demo terms text.",
    published_at: "2026-11-04T00:00:00Z",
  },
  {
    kind: "privacy",
    locale: "en",
    version: 1,
    body: "Demo text, not legal advice; replace before launch.\n\n# Axiom Privacy Policy (Demo)\n\n## Introduction and Scope\n\nThis is demo privacy text.",
    published_at: "2026-11-04T00:00:00Z",
  },
];

export const goodInvite = "INVITE-OK";

const usd = (units: string): Schemas["Money"] => ({ amount: `${units}000000000`, currency: "USD" });
const nano = (amount: string): Schemas["Money"] => ({ amount, currency: "USD" });

const keyId = (n: number) => `key_${String(n).padStart(26, "0")}` as Schemas["KeyId"];
const callId = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const workspace = `ws_${"0".repeat(26)}` as Schemas["WorkspaceId"];

// The only workspace of the mocked account, its default, which holds every key: the console shows no workspace then.
export const defaultWorkspace: Schemas["Workspace"] = {
  id: workspace,
  name: "Default",
  is_default: true,
  disabled: false,
};

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

const rules = (value: object) => value as Schemas["RoutingRules"];
const presetId = (n: number) => `00000000-0000-4000-9000-${String(n).padStart(12, "0")}`;

export function aPreset(n: number, over: Partial<Schemas["RoutingPreset"]> = {}): Schemas["RoutingPreset"] {
  return {
    id: presetId(n),
    scope: "account",
    slug: `preset-${n}`,
    name: `Preset ${n}`,
    revision: 1,
    version: 1,
    rules: rules({ default: {} }),
    disabled: false,
    created_at: "2026-11-01T08:00:00Z",
    updated_at: "2026-11-01T08:00:00Z",
    ...over,
  };
}

const models = ["anthropic/claude-sonnet-4", "openai/gpt-5", "google/gemini-2.5-pro", "deepseek/deepseek-v3"];

function aCall(n: number, over: Partial<Schemas["Call"]> = {}): Schemas["Call"] {
  return buildMockCall(n, workspace, over);
}

// tokenRate is a per-token rate as a node sends it: the amount per million tokens to show (the amount with its point
// moved six places in text) and whether a list shows it as the item's price (#695).
const tokenRate = (
  rate: Pick<Schemas["ModelRate"], "service_tier" | "above" | "item" | "amount">,
): Schemas["ModelRate"] => {
  const [whole = "0", fraction = ""] = rate.amount.split(".");
  const shifted = `${whole}${fraction.padEnd(6, "0").slice(0, 6)}`.replace(/^0+(?=\d)/, "");
  return {
    ...rate,
    unit: "token",
    headline: rate.above === undefined && rate.service_tier === "standard",
    display_amount: fraction.length > 6 ? `${shifted}.${fraction.slice(6)}` : shifted,
    display_per: 1000000,
  };
};

function aModel(n: number, over: Partial<Schemas["PublicModel"]> = {}): Schemas["PublicModel"] {
  const abilities: NonNullable<Schemas["PublicModel"]["abilities"]> =
    n % 2 === 0 ? ["tools", "reasoning"] : ["tools", "images"];
  const pricing: Schemas["ModelPricing"] = {
    currency: "USD",
    basis: ["input", "output"],
    rates: [
      tokenRate({ service_tier: "standard", item: "input", amount: `0.00000${1 + (n % 9)}` }),
      tokenRate({ service_tier: "standard", above: 128000, item: "input", amount: "0.00001" }),
      tokenRate({ service_tier: "standard", item: "output", amount: `0.00000${2 + (n % 9)}` }),
      tokenRate({ service_tier: "priority", above: 200000, item: "output", amount: "0.00002" }),
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
          tokenRate({ service_tier: "standard", item: "input", amount: `0.00000${4 + (n % 9)}` }),
          tokenRate({ service_tier: "standard", item: "output", amount: `0.00000${5 + (n % 9)}` }),
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

// The system preset, which new keys take when no layer sets a default.
export const systemPreset = "00000000-0000-0000-0000-000000000000";

const onDefault = {
  preset_id: presetId(3),
  preset: { id: presetId(3), name: "Official, cheapest first", scope: "account", version: 2 },
} as const;

// A key bound to the platform's preset while new keys take the account's default, so the list says so.
const offDefault = {
  preset_id: presetId(1),
  preset: { id: presetId(1), name: "Official only", scope: "platform", version: 1 },
} as const;

// The platform's, the site's and the account's presets, the account's being its default and the CI runner's; the
// Laptop key is bound to the platform's, made before the default changed.
function usualPresets(): Pick<MockState, "presets" | "routingDefault"> {
  return {
    presets: [
      aPreset(1, {
        scope: "platform",
        slug: "official",
        name: "Official only",
        keys: 1,
        rules: rules({ default: { classes: ["official"], min_score: 90 } }),
      }),
      aPreset(2, {
        scope: "site",
        slug: "verified",
        name: "Verified",
        rules: rules({ default: { verified_only: true } }),
      }),
      aPreset(3, {
        name: "Official, cheapest first",
        version: 2,
        keys: 1,
        rules: rules({
          default: { sort: "price", classes: ["official", "cloud"], max_price: { prompt: "3", completion: "15" } },
          rules: [{ author: "anthropic", sort: "quality" }],
        }),
      }),
    ],
    routingDefault: { preset_id: presetId(3), version: 4, new_keys_preset_id: presetId(3) },
  };
}

export function usualState(): MockState {
  return {
    ...authState(allPermissions),
    ...mailedLinks(),
    registered: new Map(),
    site: {
      name: "Axiom",
      currency: "USD",
      key_prefix: "sk-ax-v1-",
      api_base_url: "https://api.example.com",
      registration: "invite",
    },
    legal: [],
    legalAccepted: false,
    ...usualAccountFields(),
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
      aKey(3, {
        name: "Laptop",
        spend_limit: usd("20"),
        limit_reset: "monthly",
        limit_remaining: usd("17"),
        ...offDefault,
      }),
      aKey(2, {
        name: "CI runner",
        disabled: true,
        models: ["openai/gpt-5"],
        ip_allow: ["203.0.113.0/24"],
        ...onDefault,
      }),
      aKey(1, { name: "Old key", expires_at: "2026-09-01T00:00:00Z" }),
    ],
    passkeys: [],
    calls: range(120).map((n) => aCall(n)),
    models: range(4).map((n) => aModel(n)),
    statement: range(120).map((n) => aMovement(n)),
    orders: range(12).map((n) => anOrder(n)),
    ...usualBillState(),
    ...usualPresets(),
  };
}

export function emptyState(): MockState {
  const state = {
    ...usualState(),
    ...emptyAccountFields(),
    keys: [],
    calls: [],
    models: [],
    statement: [],
    orders: [],
    methods: [],
    presets: [],
    ...emptyBillState(),
    routingDefault: { version: 0, new_keys_preset_id: systemPreset },
  };
  state.balance = { ...state.balance, batches: [] };
  return state;
}

const long = (text: string, length: number) => text.repeat(Math.ceil(length / text.length)).slice(0, length);

// The longest values the contract allows, mixed over the rows: a 100-character key name, 200-character model names, a
// 254-character email, a 200-character API address, the biggest amounts, a debt, and a lot of rows.
export function worstState(): MockState {
  const state = { ...usualState(), ...worstAccountFields(long) };
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
  worstPresets(state);
  return state;
}

// The most presets a key may choose from (the platform's and the site's 50 live each, the account's 20, a workspace's
// 10), with the longest names and the most overrides a rule may hold. The account's first preset also has the most keys
// and is its default, so its disable dialog says everything it can.
function worstPresets(state: MockState) {
  const scope = (n: number) => (n < 50 ? "platform" : n < 100 ? "site" : n < 120 ? "account" : "workspace");
  state.presets = range(130).map((n) =>
    aPreset(n + 10, {
      scope: scope(n),
      slug: long(`preset-${n}-`, 64),
      name:
        n === 1 || n === 100 ? long("Official sources at official quality, cheapest first ", 100) : `プリセット ${n}`,
      version: Number.MAX_SAFE_INTEGER,
      keys: n === 100 ? Number.MAX_SAFE_INTEGER : undefined,
      rules: rules({
        default: {
          sort: "throughput",
          classes: range(32).map((c) => `class-${c}`),
          verified_only: true,
          min_score: 100,
        },
        rules: range(100).map((r) => ({ model: `vendor/${long("model-", 100)}${r}` })),
      }),
    }),
  );
  const preset = {
    id: presetId(11),
    name: long("Preset ", 100),
    scope: "platform",
    version: 1,
    disabled: true,
  } as const;
  state.keys = state.keys.map((key, n) => (n === 0 ? { ...key, preset_id: preset.id, preset } : key));
  state.routingDefault = {
    preset_id: presetId(110),
    version: Number.MAX_SAFE_INTEGER,
    new_keys_preset_id: presetId(110),
  };
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
  state.statementDaily = worstBillDaily();
  state.notifications = {
    ...state.notifications,
    threshold: "9223372036854775807",
    webhook_url: `https://${host}.example.com/hooks/axiom`,
  };
}
