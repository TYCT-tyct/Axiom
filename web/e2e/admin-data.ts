import type { components } from "../packages/api/src/admin.gen.ts";
import type { AuthState } from "./mock.ts";
import { accountId, authState, userId } from "./mock-auth.ts";

// The data the mocked admin API starts from, written as the contract's own types, so a field the contract changes stops
// this file compiling. Three sets: a usual platform, an empty one, and the worst realistic one (the longest values the
// contract allows, the biggest amounts, the most rows), which the screenshots of the acceptance gate use.

export type Schemas = components["schemas"];

export type AdminState = AuthState & {
  siteId: Schemas["SiteId"];
  settings: Schemas["SiteSettingsView"];
  tiers: Schemas["TierRef"][];
  users: Schemas["UserSummary"][];
  accounts: Map<string, Schemas["AccountDetail"]>;
  suppliers: Schemas["SupplierDetail"][];
  // how many times a supplier that is connecting has been read, to settle it after the first
  reads: Map<string, number>;
  pending: Schemas["PendingMapping"][];
  authors: Schemas["Author"][];
  models: Schemas["Model"][];
  rules: Schemas["PricingRule"][];
};

const allPermissions = [
  "users.view",
  "credits.grant",
  "suppliers.view",
  "suppliers.manage",
  "catalog.view",
  "catalog.manage",
  "pricing.view",
  "pricing.manage",
  "sites.manage",
];

const pad = (n: number, width = 26) => String(n).padStart(width, "0");
export const uuid = (kind: number, n: number) => `${pad(kind, 8)}-0000-4000-8000-${pad(n, 12)}`;
const tierId = (n: number) => `tier_${pad(n)}` as Schemas["TierId"];
const siteId = `site_${pad(1)}` as Schemas["SiteId"];
const usd = (units: string): Schemas["Money"] => ({ amount: `${units}000000000`, currency: "USD" });
const nano = (amount: string): Schemas["Money"] => ({ amount, currency: "USD" });
const range = (count: number) => Array.from({ length: count }, (_, i) => i + 1);
const long = (text: string, length: number) => text.repeat(Math.ceil(length / text.length)).slice(0, length);

const tiers: Schemas["TierRef"][] = [
  { id: tierId(1), name: "Standard" },
  { id: tierId(2), name: "Pro" },
  { id: tierId(3), name: "Friends" },
];

function aUser(n: number, over: Partial<Schemas["UserSummary"]> = {}): Schemas["UserSummary"] {
  return {
    id: userId(n % 10),
    account_id: `acct_${pad(n)}` as Schemas["AccountId"],
    site_id: siteId,
    email: `user${n}@example.com`,
    status: n % 11 === 0 ? "disabled" : "active",
    tier: tiers[n % 3] ?? tiers[0] ?? { id: tierId(1), name: "Standard" },
    mfa_enabled: n % 3 === 0,
    created_at: new Date(Date.UTC(2026, 9, 1, 8, n)).toISOString(),
    ...over,
  };
}

function anAccount(user: Schemas["UserSummary"], over: Partial<Schemas["AccountDetail"]> = {}) {
  return {
    id: user.account_id,
    site_id: user.site_id,
    name: user.email,
    kind: "personal",
    status: user.status,
    tier: user.tier,
    owner: { id: user.id, email: user.email },
    balance: {
      balance: usd("12"),
      reserved: nano("250000000"),
      floor: usd("0"),
      available: nano("11750000000"),
      spent_this_month: nano("3141592653"),
    },
    created_at: user.created_at,
    ...over,
  } satisfies Schemas["AccountDetail"];
}

export function aSupplier(n: number, over: Partial<Schemas["Supplier"]> = {}): Schemas["Supplier"] {
  return {
    id: uuid(2, n),
    name: `Supplier ${n}`,
    kind: n % 2 === 0 ? "sub2api" : "newapi",
    origin: `https://api${n}.example.com`,
    upstream_account: `ops${n}@example.com`,
    generation: "1.0",
    detected_version: "1.4.2",
    native_unit: n % 2 === 0 ? "usd" : "quota",
    settlement_currency: n % 2 === 0 ? "USD" : "CNY",
    status: "active",
    balance_native: "1520.5",
    balance_observed_at: "2026-10-15T07:30:00Z",
    low_balance_threshold: "100",
    unit_value: n % 2 === 0 ? "1" : "0.0000142",
    ...over,
  };
}

function aGroup(supplier: number, n: number, over: Partial<Schemas["SupplierGroup"]> = {}): Schemas["SupplierGroup"] {
  return {
    id: uuid(3, supplier * 100 + n),
    upstream_ref: `group-${n}`,
    name: n === 1 ? "default" : `vip-${n}`,
    ratio: n === 1 ? "1" : "1.5",
    selected: n === 1,
    codename: n === 1 ? "Maple" : undefined,
    present: true,
    ...over,
  };
}

function aDetail(n: number, over: Partial<Schemas["Supplier"]> = {}): Schemas["SupplierDetail"] {
  const supplier = aSupplier(n, over);
  const groups = [aGroup(n, 1), aGroup(n, 2)];
  return {
    supplier,
    groups,
    credentials: [
      {
        id: uuid(4, n),
        group_id: groups[0]?.id ?? "",
        upstream_key_ref: `key-${n}-a`,
        status: "active",
        max_concurrency: 8,
      },
    ],
    unbound_keys: [{ ref: `key-${n}-b`, group_ref: "group-2", status: "active" }],
  };
}

function suppliers(): Schemas["SupplierDetail"][] {
  return [
    aDetail(1),
    aDetail(2, { name: "Example US", unit_value: undefined, unit_value_hint: "1" }),
    aDetail(3, {
      name: "Broken Upstream",
      status: "needs_attention",
      status_reason: "The supplier rejected the login (401).",
    }),
    aDetail(4, { name: "Paused Site", status: "paused" }),
    aDetail(5, { name: "Low On Funds", status: "low_balance", balance_native: "40" }),
  ];
}

function pending(count: number): Schemas["PendingMapping"][] {
  return range(count).map((n) => ({
    supplier_id: uuid(2, (n % 3) + 1),
    group_id: uuid(3, ((n % 3) + 1) * 100 + 1),
    group_codename: "Maple",
    upstream_name: `vendor-model-${n}-preview`,
    formats: n % 2 === 0 ? ["messages", "chat"] : ["responses"],
  }));
}

const authors: Schemas["Author"][] = [
  { slug: "anthropic", name: "Anthropic" },
  { slug: "openai", name: "OpenAI" },
  { slug: "google", name: "Google" },
  { slug: "deepseek", name: "DeepSeek" },
];

function aModel(n: number, over: Partial<Schemas["Model"]> = {}): Schemas["Model"] {
  const slug = `${authors[n % authors.length]?.slug ?? "anthropic"}/model-${n}` as Schemas["ModelSlug"];
  return {
    id: uuid(5, n),
    slug,
    author: (authors[n % authors.length]?.slug ?? "anthropic") as Schemas["AuthorSlug"],
    status: "listed",
    version_basis: "dated",
    price_source: "official",
    rev: 3,
    spec: { items: ["input", "output", "cache_read"], tiers: ["standard"], context_length: 200000 },
    names: [{ name: slug, visibility: "public" }],
    prices: [
      {
        id: uuid(6, n),
        currency: "USD",
        source: "official",
        published_at: "2026-09-20T10:00:00Z",
        schedule: {
          v: 1,
          currency: "USD",
          rates: { input: "0.000003", output: "0.000015", cache_read: "0.0000003" },
        },
      },
    ],
    ...over,
  };
}

// A draft: it has no spec, name or price yet, so it cannot go on sale.
const aDraft = (n: number) => aModel(n, { status: "draft", spec: {}, names: [], prices: [], rev: 0 });

function models(): Schemas["Model"][] {
  return [
    aModel(1),
    aModel(2, { price_source: "manual" }),
    aDraft(3),
    aModel(4, { status: "retired" }),
    aModel(5, { price_source: "litellm" }),
    ...range(20).map((n) => aModel(n + 10)),
  ];
}

function rules(): Schemas["PricingRule"][] {
  const base = {
    rev: 1,
    updated_at: "2026-10-10T09:00:00Z",
    constraints: {},
  } as const;
  return [
    {
      ...base,
      id: uuid(7, 1),
      enabled: true,
      target: { kind: "model", id: uuid(5, 1) },
      audience: { kind: "tier", id: tierId(2) },
      method: { kind: "reference_multiplier", multiplier: "1.3", currency_conversion: "face" },
      constraints: { min_margin_ratio: "0.1" },
    },
    {
      ...base,
      id: uuid(7, 2),
      enabled: true,
      target: { kind: "author", id: "anthropic" },
      audience: { kind: "all" },
      method: { kind: "reference_multiplier", multiplier: "1.1", currency_conversion: "face" },
    },
    {
      ...base,
      id: uuid(7, 3),
      enabled: false,
      target: { kind: "all" },
      audience: { kind: "all" },
      method: { kind: "reference_multiplier", multiplier: "1.2", currency_conversion: "face" },
    },
  ];
}

export function usualState(): AdminState {
  const users = range(60).map((n) => aUser(n));
  const own = aUser(0, { email: "admin@example.com", mfa_enabled: true, tier: tiers[0] });
  const state: AdminState = {
    ...authState(allPermissions),
    siteId,
    settings: {
      settings: { console_idle_timeout_seconds: 604800, new_key_daily_limit: usd("20") },
      defaults: {
        console_idle_timeout_seconds: 172800,
        console_max_age_seconds: 2592000,
        admin_idle_timeout_seconds: 3600,
        admin_max_age_seconds: 43200,
        new_key_daily_limit: usd("10"),
        max_keys_per_account: 100,
        key_creations_per_hour: 50,
      },
    },
    tiers,
    users,
    accounts: new Map([own, ...users].map((user) => [user.account_id, anAccount(user)])),
    suppliers: suppliers(),
    reads: new Map(),
    pending: pending(60),
    authors: [...authors],
    models: models(),
    rules: rules(),
  };
  state.user.email = "admin@example.com";
  state.accounts.set(accountId(0), anAccount(own));
  return state;
}

export function emptyState(): AdminState {
  return { ...usualState(), users: [], suppliers: [], pending: [], authors: [], models: [], rules: [] };
}

// The longest values the contract allows, mixed over the rows: a 200-character supplier name, a 254-character email,
// 182-character model slugs, the biggest amounts, and a lot of rows.
export function worstState(): AdminState {
  const state = usualState();
  const email = `${long("bartholomew.fitzgerald-", 200)}@northwind-industries-holdings.example.com`;
  state.users = [aUser(1, { email }), aUser(2, { email: "J@x.io" }), ...range(150).map((n) => aUser(n + 2))];
  for (const user of state.users) {
    state.accounts.set(
      user.account_id,
      anAccount(user, {
        name: long("Northwind ", 100),
        tier: { id: tierId(9), name: long("Enterprise-", 100) },
        balance: {
          balance: nano("-1500000000"),
          reserved: nano("9223372036854775807"),
          floor: nano("-5000000000"),
          available: nano("-9223372036854775807"),
          spent_this_month: nano("9223372036854775807"),
        },
      }),
    );
  }
  state.suppliers = [
    aDetail(1, {
      name: long("Supplier with a name of the longest length ", 200),
      origin: `https://${long("a-very-long-host-", 200)}.example.com`,
      upstream_account: long("ops-", 254),
      balance_native: "999999999999999999.999999999999999",
      status: "needs_attention",
      status_reason: long("The supplier rejected the login. ", 600),
    }),
    ...range(60).map((n) => aDetail(n + 1)),
  ];
  const slug = `${long("a-vendor-with-a-long-name", 40).replace(/-$/, "a")}/${long("model-name-", 99)}@${long("v", 39)}`;
  state.models = [
    aModel(1, { slug: slug as Schemas["ModelSlug"], names: [{ name: long("alias-", 200), visibility: "internal" }] }),
    ...range(150).map((n) => (n === 2 ? aDraft(3) : aModel(n + 1))),
  ];
  state.pending = pending(150).map((row, i) => (i === 0 ? { ...row, upstream_name: long("up-", 200) } : row));
  return state;
}
