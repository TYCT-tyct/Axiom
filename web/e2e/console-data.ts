import type { components } from "../packages/api/src/console.gen.ts";

// The data the mocked console API starts from, written as the contract's own types, so a field the contract changes
// stops this file compiling. Three sets: a usual account, an empty one, and the worst realistic one (the longest values
// the contract allows, the biggest amounts, the most items), which the screenshots of the acceptance gate use.

export type Schemas = components["schemas"];
export type Level = "none" | "pending_mfa" | "password" | "mfa";

export type MockState = {
  level: Level;
  // which account is signed in: the number its user and account IDs are made of, so a test can hand the browser's
  // session to another account the way a second tab does
  account: number;
  csrf: string;
  csrfCount: number;
  user: { email: string; password: string; inviteCode: string; mfaCode: string; recoveryCode: string };
  mfaEnabled: boolean;
  permissions: string[];
  site: Schemas["Site"];
  balance: Schemas["Balance"];
  keys: Schemas["Key"][];
  calls: Schemas["Call"][];
  version: string;
  // every request the page made, as "METHOD /path", for tests that count them
  log: string[];
};

const allPermissions = ["api_keys.view", "api_keys.manage", "balance.view", "calls.view"];
export const knownEmail = "user@example.com";
export const knownPassword = "correct horse battery";
export const mfaEmail = "mfa@example.com";
export const limitedEmail = "limited@example.com";
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

const range = (count: number) => Array.from({ length: count }, (_, i) => i + 1);

export function usualState(): MockState {
  return {
    level: "none",
    account: 0,
    csrf: "",
    csrfCount: 0,
    user: {
      email: knownEmail,
      password: knownPassword,
      inviteCode: "AXIOM-7K2Q",
      mfaCode: "123456",
      recoveryCode: "aaaa-bbbb-cccc-dddd",
    },
    mfaEnabled: false,
    permissions: allPermissions,
    site: { name: "Axiom", key_prefix: "sk-ax-v1-", registration: "invite" },
    balance: {
      balance: usd("12"),
      reserved: nano("250000000"),
      floor: usd("0"),
      available: nano("11750000000"),
      spent_this_month: nano("3141592653"),
    },
    keys: [
      aKey(3, { name: "Laptop", spend_limit: usd("20"), limit_reset: "monthly", limit_remaining: usd("17") }),
      aKey(2, { name: "CI runner", disabled: true, models: ["openai/gpt-5"], ip_allow: ["203.0.113.0/24"] }),
      aKey(1, { name: "Old key", expires_at: "2026-09-01T00:00:00Z" }),
    ],
    calls: range(120).map((n) => aCall(n)),
    version: process.env.AXIOM_VERSION ?? "",
    log: [],
  };
}

export function emptyState(): MockState {
  return { ...usualState(), keys: [], calls: [] };
}

const long = (text: string, length: number) => text.repeat(Math.ceil(length / text.length)).slice(0, length);

// The longest values the contract allows, mixed over the rows: a 100-character key name, 200-character model names, a
// 254-character email, the biggest amounts, a debt, and a lot of rows.
export function worstState(): MockState {
  const state = usualState();
  state.user.email = `${long("bartholomew.fitzgerald-", 200)}@northwind-industries-holdings.example.com`;
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
  return state;
}
